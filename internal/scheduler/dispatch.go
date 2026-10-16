package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"time"
)

// Reconcile runs before the timer loop starts, after feature/job recovery.
// Unclaimed triggers missed while offline are skipped as one durable report.
// A pre-crash claimed trigger remains accepted intent and is retried by key.
func (s *Service) Reconcile(ctx context.Context) error {
	// Drain every batch against one startup cutoff. Re-reading the wall clock
	// between batches could consume future live triggers or leave downtime work
	// for the normal timer to admit once queue dispatch resumes.
	cutoff := s.now()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := s.claimDueBatch(ctx, true, cutoff)
		if err != nil {
			return err
		}
		if count < 100 {
			return nil
		}
	}
}
func (s *Service) Tick(ctx context.Context) error {
	if e := s.claimDue(ctx, false); e != nil {
		return e
	}
	if e := s.dispatchClaims(ctx); e != nil {
		return e
	}
	if e := s.ObserveRecovery(ctx); e != nil {
		return e
	}
	return s.dispatchRecovery(ctx)
}

// Run starts no hidden goroutine; cancellation stops future admission. The
// integrator must call Reconcile once before this loop and before enabling UI.
func (s *Service) Run(ctx context.Context) error {
	if e := s.Tick(ctx); e != nil {
		s.config.Log.Error("scheduler tick failed; durable intent retained")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if e := s.Tick(ctx); e != nil {
				s.config.Log.Error("scheduler tick failed; durable intent retained")
			}
		}
	}
}
func (s *Service) claimDue(ctx context.Context, recovering bool) error {
	_, err := s.claimDueBatch(ctx, recovering, s.now())
	return err
}
func (s *Service) claimDueBatch(ctx context.Context, recovering bool, now time.Time) (int, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT id,instance_id,actor_id,definition_json,next_run_at FROM schedules WHERE enabled=1 AND next_run_at!='' AND next_run_at<=? ORDER BY next_run_at,id LIMIT 100`, stamp(now))
	if e != nil {
		return 0, e
	}
	type due struct {
		id, instance, raw, next string
		actor                   int64
	}
	pending := []due{}
	for rows.Next() {
		var d due
		if e = rows.Scan(&d.id, &d.instance, &d.actor, &d.raw, &d.next); e != nil {
			rows.Close()
			return 0, e
		}
		pending = append(pending, d)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return 0, e
	}
	for _, d := range pending {
		var definition domain.Schedule
		if e = json.Unmarshal([]byte(d.raw), &definition); e != nil {
			return 0, e
		}
		next, e := Next(definition.Expression, definition.Timezone, now)
		if e != nil {
			return 0, e
		}
		dueAt, e := time.Parse(time.RFC3339Nano, d.next)
		if e != nil {
			return 0, e
		}
		state, reason := "claimed", ""
		if recovering || now.Sub(dueAt) >= time.Minute {
			state, reason = "skipped_downtime", "Missed occurrences were skipped without catch-up."
		} else {
			var active bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schedule_runs r LEFT JOIN jobs j ON j.id=r.job_id WHERE r.schedule_id=? AND (r.state='claimed' OR (r.state='admitted' AND COALESCE(j.state,'queued') IN('queued','running'))))`, d.id).Scan(&active)
			if e != nil {
				return 0, e
			}
			if active {
				state, reason = "skipped_overlap", "An earlier occurrence is still pending; this occurrence was coalesced."
			}
		}
		id, e := key()
		if e != nil {
			return 0, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO schedule_runs(id,schedule_id,instance_id,actor_id,due_at,definition_json,state,reason) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(schedule_id,due_at) DO NOTHING`, id, d.id, d.instance, d.actor, d.next, d.raw, state, reason); e != nil {
			return 0, e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE schedules SET next_run_at=? WHERE id=? AND next_run_at=?`, stamp(next), d.id, d.next); e != nil {
			return 0, e
		}
	}
	if e := tx.Commit(); e != nil {
		return 0, e
	}
	return len(pending), nil
}
func (s *Service) dispatchClaims(ctx context.Context) error {
	rows, e := s.db.QueryContext(ctx, `SELECT id,schedule_id,instance_id,actor_id,definition_json FROM schedule_runs WHERE state='claimed' ORDER BY due_at,id LIMIT 100`)
	if e != nil {
		return e
	}
	type claim struct {
		id, schedule, instance, raw string
		actor                       int64
	}
	claims := []claim{}
	for rows.Next() {
		var c claim
		if e = rows.Scan(&c.id, &c.schedule, &c.instance, &c.actor, &c.raw); e != nil {
			rows.Close()
			return e
		}
		claims = append(claims, c)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	var failures []error
	for _, c := range claims {
		actor, e := s.actor(ctx, c.actor)
		var definition domain.Schedule
		if e == nil {
			e = json.Unmarshal([]byte(c.raw), &definition)
		}
		if e == nil {
			selection, selectionErr := s.selection(ctx, c.instance)
			e = selectionErr
			if e == nil {
				var input Input
				raw, _ := json.Marshal(definition)
				e = json.Unmarshal(raw, &input)
				if e == nil {
					if definition.Action != domain.ScheduleRestart {
						input.CountdownSeconds = nil
					}
					_, e = validate(input, selection, actor, s.host, s.now())
				}
			}
		}
		if e != nil {
			if markErr := s.block(ctx, c.id, c.schedule, "Schedule owner authority or current action validation failed; an operator must review and enable it."); markErr != nil {
				failures = append(failures, markErr)
			}
			continue
		}
		job, e := s.admit(ctx, "schedule:"+c.id, c.instance, definition, actor)
		if e != nil {
			code := domain.AsError(e).Code
			if code == domain.CodeForbidden || code == domain.CodeValidationFailed || code == domain.CodeUnsupported {
				failures = append(failures, s.block(ctx, c.id, c.schedule, "Scheduled action was refused by its feature service; review before enabling."))
			} else {
				failures = append(failures, fmt.Errorf("admit scheduled action: %w", e))
			}
			continue
		}
		if job.ID == "" {
			failures = append(failures, domain.E(domain.CodeUnavailable, "feature admission returned no durable job"))
			continue
		}
		_, e = s.db.ExecContext(ctx, `UPDATE schedule_runs SET state='admitted',job_id=? WHERE id=? AND state='claimed'`, job.ID, c.id)
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) block(ctx context.Context, run, schedule, reason string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `UPDATE schedule_runs SET state='blocked',reason=? WHERE id=? AND state='claimed'`, reason, run); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE schedules SET enabled=0,next_run_at='',disabled_reason=? WHERE id=?`, reason, schedule); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Service) admit(ctx context.Context, key, id string, d domain.Schedule, u domain.User) (jobs.Job, error) {
	if !s.available(d.Action) {
		return jobs.Job{}, domain.E(domain.CodeUnavailable, "typed schedule producer is not configured")
	}
	switch d.Action {
	case domain.ScheduleBackup:
		return s.config.Backups.EnqueueAutomaticOnce(ctx, key, id, d.BackupKind, u)
	case domain.ScheduleCommand:
		return s.config.Commands.EnqueueCommandOnce(ctx, key, id, d.Command, u)
	case domain.ScheduleAnnouncement:
		return s.config.Commands.EnqueueCommandOnce(ctx, key, id, "say "+d.Announcement, u)
	case domain.ScheduleRestart:
		return s.config.Restarts.EnqueueRestartOnce(ctx, key, id, d.CountdownSeconds, u)
	case domain.ScheduleUpdate:
		return s.config.Updates.EnqueueAutomaticOnce(ctx, key, id, u)
	}
	return jobs.Job{}, domain.E(domain.CodeUnsupported, "unknown schedule action")
}
