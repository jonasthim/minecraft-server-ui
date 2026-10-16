package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"time"
)

func validEvent(e Event) bool {
	return e.ID != "" && len(e.ID) <= 160 && domain.ValidateInstanceID(e.InstanceID) == nil && (e.Kind == JobFailed || e.Kind == JobSucceeded) && allowedOperation(e.Operation) && !e.CreatedAt.IsZero()
}
func allowedOperation(op string) bool {
	switch op {
	case "backup_create", "backup_restore", "lifecycle", "content_mutation", "modpack_apply", "update_apply":
		return true
	}
	return false
}

// Enqueue accepts a finite typed event for future canonical producers. It never
// accepts transport destinations, arbitrary text, or secrets in event data.
func (s *Service) Enqueue(ctx context.Context, event Event) error {
	if !validEvent(event) {
		return domain.E(domain.CodeValidationFailed, "invalid notification event")
	}
	_, err := s.cfg.DB.ExecContext(ctx, `INSERT OR IGNORE INTO notification_events(id,instance_id,kind,operation,created_at)VALUES(?,?,?,?,?)`, event.ID, event.InstanceID, event.Kind, event.Operation, event.CreatedAt.Unix())
	return err
}
func (s *Service) processOutbox(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.cfg.DB.QueryContext(ctx, `SELECT seq,id,instance_id,kind,operation,created_at FROM notification_events WHERE processed=0 ORDER BY created_at,id LIMIT 100`)
	if err != nil {
		return err
	}
	type queuedEvent struct {
		Event
		seq int64
	}
	events := []queuedEvent{}
	for rows.Next() {
		var event queuedEvent
		var at int64
		if err = rows.Scan(&event.seq, &event.ID, &event.InstanceID, &event.Kind, &event.Operation, &at); err != nil {
			break
		}
		event.CreatedAt = time.Unix(at, 0).UTC()
		events = append(events, event)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	channels, err := s.List(ctx)
	if err != nil {
		return err
	}
	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.cfg.Now().Unix()
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_deliveries WHERE status IN ('queued','sending')`).Scan(&pending); err != nil {
		return err
	}
	for _, event := range events {
		if validEvent(event.Event) {
			for _, c := range channels {
				if !c.Enabled {
					continue
				}
				selected := false
				for _, kind := range c.Events {
					if kind == event.Kind {
						selected = true
					}
				}
				if !selected {
					continue
				}
				var floor int64
				if err = tx.QueryRowContext(ctx, `SELECT event_floor FROM notification_channels WHERE id=?`, c.ID).Scan(&floor); err != nil {
					return err
				}
				if event.seq <= floor {
					continue
				}
				status, failure := "queued", ""
				if pending >= MaxQueue {
					status, failure = "failed", "queue_full"
				} else {
					pending++
				}
				if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO notification_deliveries(channel_id,event_id,provider,instance_id,event_kind,status,next_attempt,created_at,updated_at,safe_failure)VALUES(?,?,?,?,?,?,?,?,?,?)`, c.ID, event.ID, c.Provider, event.InstanceID, event.Kind, status, now, now, now, failure); err != nil {
					return err
				}
			}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE notification_events SET processed=1 WHERE id=?`, event.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Service) recover(ctx context.Context) error {
	// A crashed sender may have delivered the message before recording success.
	// Never replay that attempt automatically; the durable status remains honest.
	_, err := s.cfg.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status='unknown_after_crash',safe_failure='delivery_outcome_unknown',updated_at=? WHERE status='sending'`, s.cfg.Now().Unix())
	return err
}
func (s *Service) deliverOne(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int64
	var channel, eventID string
	err := s.cfg.DB.QueryRowContext(ctx, `SELECT id,channel_id,event_id FROM notification_deliveries WHERE status='queued' AND next_attempt<=? ORDER BY next_attempt,id LIMIT 1`, s.cfg.Now().Unix()).Scan(&id, &channel, &eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := s.load(ctx, channel)
	if err != nil || !c.Enabled {
		_, err = s.cfg.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status='failed',safe_failure='channel_unavailable',updated_at=? WHERE id=?`, s.cfg.Now().Unix(), id)
		return true, err
	}
	var event Event
	var at int64
	err = s.cfg.DB.QueryRowContext(ctx, `SELECT id,instance_id,kind,operation,created_at FROM notification_events WHERE id=?`, eventID).Scan(&event.ID, &event.InstanceID, &event.Kind, &event.Operation, &at)
	event.CreatedAt = time.Unix(at, 0).UTC()
	if err != nil {
		return false, err
	}
	if !validEvent(event) {
		return false, fmt.Errorf("stored notification event invalid")
	}
	now := s.cfg.Now().Unix()
	_, err = s.cfg.DB.ExecContext(ctx, `UPDATE notification_deliveries SET status='sending',attempts=attempts+1,updated_at=? WHERE id=?`, now, id)
	if err != nil {
		return false, err
	}
	timeout, cancel := context.WithTimeout(ctx, DeliveryTimeout)
	deliveryErr := s.transport(timeout, c, event)
	cancel()
	// Use a bounded detached persistence context only after transport has ended;
	// Run still waits for it before returning and database close remains ordered.
	persist, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer done()
	var attempts int
	if err = s.cfg.DB.QueryRowContext(persist, `SELECT attempts FROM notification_deliveries WHERE id=?`, id).Scan(&attempts); err != nil {
		return true, err
	}
	status, failure, next := "sent", "", s.cfg.Now().Unix()
	if deliveryErr != nil {
		status = "failed"
		failure = "transport_failed"
		var safe safeFailure
		if errors.As(deliveryErr, &safe) {
			failure = string(safe)
		}
		if attempts < 3 && ctx.Err() == nil && failure != "configuration_invalid" && failure != "destination_rejected" && failure != "redirect_rejected" && failure != "authentication_rejected" {
			status = "queued"
			delay := 30 * time.Second
			if attempts > 1 {
				delay = 2 * time.Minute
			}
			next = s.cfg.Now().Add(delay).Unix()
		}
	}
	if ctx.Err() != nil && deliveryErr != nil {
		status = "unknown_after_crash"
		failure = "delivery_outcome_unknown"
	}
	_, err = s.cfg.DB.ExecContext(persist, `UPDATE notification_deliveries SET status=?,safe_failure=?,next_attempt=?,updated_at=? WHERE id=?`, status, failure, next, s.cfg.Now().Unix(), id)
	return true, err
}
func (s *Service) Retain(ctx context.Context) error {
	cutoff := s.cfg.Now().Add(-HistoryRetention).Unix()
	tx, err := s.cfg.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Preserve pending outbox/deliveries until terminal; hard queue admission bounds
	// storage independently of history age. Old queued items expire explicitly.
	if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='failed',safe_failure='expired',updated_at=? WHERE status='queued' AND created_at<?`, s.cfg.Now().Unix(), cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_deliveries WHERE status NOT IN ('queued','sending') AND (created_at<? OR id NOT IN (SELECT id FROM notification_deliveries ORDER BY id DESC LIMIT 10000))`, cutoff); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_events WHERE processed=1 AND created_at<? AND NOT EXISTS(SELECT 1 FROM notification_deliveries d WHERE d.event_id=notification_events.id)`, cutoff); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) History(ctx context.Context, before int64, limit int) (History, error) {
	out := History{Deliveries: []Delivery{}}
	if before < 0 || limit < 1 || limit > 100 {
		return out, domain.E(domain.CodeValidationFailed, "invalid notification history cursor or limit")
	}
	if before == 0 {
		before = 1 << 62
	}
	rows, err := s.cfg.DB.QueryContext(ctx, `SELECT id,channel_id,provider,instance_id,event_kind,status,attempts,created_at,updated_at,safe_failure FROM notification_deliveries WHERE id<? ORDER BY id DESC LIMIT ?`, before, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var d Delivery
		var created, updated int64
		if err = rows.Scan(&d.ID, &d.ChannelID, &d.Provider, &d.InstanceID, &d.EventKind, &d.Status, &d.Attempts, &created, &updated, &d.SafeFailure); err != nil {
			return out, err
		}
		d.CreatedAt = time.Unix(created, 0).UTC()
		d.UpdatedAt = time.Unix(updated, 0).UTC()
		out.Deliveries = append(out.Deliveries, d)
		out.NextCursor = d.ID
	}
	return out, rows.Err()
}
func (s *Service) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	if s.cfg.DB == nil {
		return fmt.Errorf("notification database unavailable")
	}
	if err := s.recover(ctx); err != nil {
		return fmt.Errorf("recover notifications: %w", err)
	}

	lastRetention := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := s.processOutbox(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("queue notifications: %w", err)
		}
		if _, err := s.deliverOne(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("persist notification delivery: %w", err)
		}
		if s.cfg.Now().Sub(lastRetention) >= time.Hour {
			if err := s.Retain(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("retain notifications: %w", err)
			}
			lastRetention = s.cfg.Now()
		}
		// Single global attempt per second, including after restart. No catch-up burst.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}
