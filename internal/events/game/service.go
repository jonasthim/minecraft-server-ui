package game

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type Logs interface {
	History(context.Context, string, int64, int) (console.Page, error)
	Status(context.Context, string) (console.State, error)
}
type Instances interface {
	Get(context.Context, string) (instance.Detail, error)
}
type Service struct {
	db        *sql.DB
	logs      Logs
	instances Instances
	mu        sync.Mutex
}

func New(database *sql.DB, logs Logs, instances Instances) *Service {
	return &Service{db: database, logs: logs, instances: instances}
}

const storageTime = "2006-01-02T15:04:05.000000000Z"

const limitations = "Partial native English log patterns only: disabled death/advancement announcements, team death-message visibility, other death templates, localized/custom/plugin output or decorated display names, redaction, rotation and manager downtime can omit events. Historical emitting version and server date/time are unavailable. Names never establish UUIDs or online account identity. 26.3 uses the reviewed System chat prefix; other changed or missing log formats remain gaps. Native pattern matches do not authenticate the emitter; plugins or data packs may imitate system messages. No add-on is assumed. Retained for 30 days, at most 20000 records including gaps per instance."

func coverage(version string, status console.State, statusErr error) Coverage {
	c := Coverage{ConfiguredVersion: version, SourceVersion: "unknown", ParserCapability: "unverified_version", ObservedAt: time.Now().UTC(), LogsObservedAt: status.LogsObservedAt, Reason: limitations}
	switch version {
	case "1.12.2", "1.21.9", "1.21.11", "26.3":
		c.ParserCapability = "static_reviewed_patterns"
		c.Available = statusErr == nil && status.LogsAvailable
	}
	if strings.HasPrefix(version, "26.3.") {
		c.ParserCapability = "unavailable_26_3"
		c.Reason = "This configured 26.3 variant has no reviewed log capability; deaths/advancements ingestion for that variant is unavailable. " + c.Reason
	} else if c.ParserCapability == "unverified_version" {
		c.Reason = "Configured version has no reviewed native log capability; any retained pattern matches remain partial historical evidence. " + c.Reason
	}
	if statusErr != nil || !status.LogsAvailable {
		c.Available = false
		c.Reason = "Live console ingestion is unavailable or stale. " + c.Reason
	}
	return c
}
func gapEvent(id string, cursor int64, at time.Time, reason string) Event {
	return Event{InstanceID: id, SourceCursor: cursor, ObservedAt: at, Kind: "source_gap", Player: console.PlayerIdentity{Authority: "unknown", Source: "redacted_native_console"}, Message: reason, Source: "redacted_native_console", SourceVersion: "unknown", ParserVersion: parserVersion, Confidence: "gap", Gap: true}
}

// Poll consumes at most 500 already-redacted durable lines, independently of the
// player history cursor. Events and cursor advance in one SQLite transaction.
func (s *Service) Poll(ctx context.Context, id string) error {
	if domain.ValidateInstanceID(id) != nil {
		return domain.E(domain.CodeValidationFailed, "invalid instance id")
	}
	if s.db == nil || s.logs == nil || s.instances == nil {
		return domain.E(domain.CodeUnavailable, "game event source unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return err
	}
	var cursor int64
	var previous string
	err = s.db.QueryRowContext(ctx, `SELECT source_cursor,availability FROM game_event_cursors WHERE instance_id=?`, id).Scan(&cursor, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read game event cursor: %w", err)
	}
	status, statusErr := s.logs.Status(ctx, id)
	c := coverage(detail.Instance.MinecraftVersion, status, statusErr)
	page, err := s.logs.History(ctx, id, cursor, 500)
	if err != nil {
		return fmt.Errorf("read redacted game event source: %w", err)
	}
	if page.NextCursor == 0 {
		c.Available = false
		c.Reason = "No retained native console output has been observed by event ingestion. " + c.Reason
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin game event ingestion: %w", err)
	}
	defer tx.Rollback()
	// Instance deletion may complete after the source page was read. The shared
	// single-connection writer pool makes this check and FK writes one admission.
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM instances WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return domain.NotFound("instance")
	} else if err != nil {
		return err
	}
	// Console History does not flag preexisting retention for a first (zero)
	// cursor. Read only its durable omission floor; no raw source is accessed.
	var consoleFloor int64
	if cursor == 0 {
		err = tx.QueryRowContext(ctx, `SELECT pruned_before FROM console_cursors WHERE instance_id=?`, id).Scan(&consoleFloor)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO game_event_cursors(instance_id) VALUES(?)`, id); err != nil {
		return err
	}
	appendEvent := func(key string, e Event) error {
		b, eerr := json.Marshal(e)
		if eerr != nil {
			return eerr
		}
		_, eerr = tx.ExecContext(ctx, `INSERT OR IGNORE INTO game_events(instance_id,source_key,source_cursor,observed_at,event_json) VALUES(?,?,?,?,?)`, id, key, e.SourceCursor, e.ObservedAt.UTC().Format(storageTime), string(b))
		return eerr
	}
	if cursor == 0 && consoleFloor > 0 {
		if err = appendEvent(fmt.Sprintf("initial-retention:%d", consoleFloor), gapEvent(id, consoleFloor, time.Now().UTC(), "Earlier console history expired before event observation began; earlier events are unknown.")); err != nil {
			return err
		}
	}
	if page.Gap {
		if err = appendEvent(fmt.Sprintf("cursor-gap:%d:%d", cursor, page.NextCursor), gapEvent(id, page.NextCursor, time.Now().UTC(), "Source cursor expired, reset, or lost retained console output; intervening events are unknown.")); err != nil {
			return err
		}
	}
	for _, line := range page.Lines {
		var e Event
		var ok bool
		if line.Gap {
			e = gapEvent(id, line.ID, line.CreatedAt, "Console output was omitted or lost through rotation, truncation or oversize limits.")
			ok = true
		} else if strings.Contains(strings.ToLower(line.Message), "[redacted]") {
			e = gapEvent(id, line.ID, line.CreatedAt, "A redacted console line was excluded from event interpretation.")
			ok = true
		} else {
			e, ok = parse(line.Message)
		}
		if !ok {
			continue
		}
		e.InstanceID = id
		e.SourceCursor = line.ID
		e.ObservedAt = line.CreatedAt
		e.Gap = e.Gap || page.Gap
		if err = appendEvent(fmt.Sprintf("line:%d", line.ID), e); err != nil {
			return fmt.Errorf("persist game event: %w", err)
		}
	}
	availability := "available"
	if !c.Available {
		availability = c.ParserCapability + ":unavailable"
	}
	if availability != "available" && availability != previous {
		if err = appendEvent(fmt.Sprintf("availability:%d:%s", page.NextCursor, availability), gapEvent(id, page.NextCursor, time.Now().UTC(), "Native event observation unavailable for the configured version or current console source; missing events are unknown.")); err != nil {
			return err
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE game_event_cursors SET source_cursor=?,availability=?,coverage_json=? WHERE instance_id=?`, page.NextCursor, availability, string(b), id); err != nil {
		return fmt.Errorf("advance game event cursor: %w", err)
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(storageTime)
	var floor int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM game_events WHERE instance_id=? AND (observed_at<? OR id NOT IN (SELECT id FROM game_events WHERE instance_id=? ORDER BY id DESC LIMIT 20000))`, id, cutoff, id).Scan(&floor); err != nil {
		return err
	}
	if floor > 0 {
		if _, err = tx.ExecContext(ctx, `DELETE FROM game_events WHERE instance_id=? AND (observed_at<? OR id NOT IN (SELECT id FROM game_events WHERE instance_id=? ORDER BY id DESC LIMIT 20000))`, id, cutoff, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE game_event_cursors SET retention_floor=MAX(retention_floor,?) WHERE instance_id=?`, floor, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit game event ingestion: %w", err)
	}
	return nil
}
func (s *Service) History(ctx context.Context, id string, after int64, limit int) (Page, error) {
	out := Page{InstanceID: id, Events: []Event{}, NextCursor: after, Gap: true, Reason: limitations, Coverage: Coverage{SourceVersion: "unknown", ParserCapability: "unverified_version", Reason: "Event observation has not started. " + limitations}}
	if domain.ValidateInstanceID(id) != nil || after < 0 || limit < 1 || limit > 200 {
		return out, domain.E(domain.CodeValidationFailed, "invalid game event cursor or limit")
	}
	if s.db == nil || s.instances == nil {
		return out, domain.E(domain.CodeUnavailable, "game event history unavailable")
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return out, err
	}
	if s.logs != nil {
		status, statusErr := s.logs.Status(ctx, id)
		out.Coverage = coverage(detail.Instance.MinecraftVersion, status, statusErr)
	} else {
		out.Coverage.ConfiguredVersion = detail.Instance.MinecraftVersion
	}
	// Keep this bounded read internally consistent while polling or retention runs.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var raw string
	var sourceCursor int64
	err = tx.QueryRowContext(ctx, `SELECT retention_floor,coverage_json,source_cursor FROM game_event_cursors WHERE instance_id=?`, id).Scan(&out.RetentionFloor, &raw, &sourceCursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	var recorded Coverage
	if raw != "" && json.Unmarshal([]byte(raw), &recorded) != nil {
		return out, domain.E(domain.CodeUnavailable, "stored event coverage invalid")
	}
	out.Coverage.ObservedAt = recorded.ObservedAt
	if sourceCursor == 0 {
		out.Coverage.Available = false
		out.Coverage.Reason = "No retained native console output has been observed by event ingestion. " + out.Coverage.Reason
	}
	if recorded.ObservedAt.IsZero() || time.Since(recorded.ObservedAt) > 15*time.Second {
		out.Coverage.Available = false
		out.Coverage.Reason = "Event ingestion has not been observed recently; displayed records are historical. " + out.Coverage.Reason
	}
	// A foreign, expired or future cursor must not suppress this instance forever.
	if after > 0 {
		var owned int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM game_events WHERE instance_id=? AND id=?`, id, after).Scan(&owned); err != nil {
			return out, err
		}
		if owned == 0 {
			after = 0
			out.NextCursor = 0
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,event_json FROM game_events WHERE instance_id=? AND id>? ORDER BY id LIMIT ?`, id, after, limit+1)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var event Event
		var b string
		if err = rows.Scan(&event.ID, &b); err != nil {
			break
		}
		eventID := event.ID
		if err = json.Unmarshal([]byte(b), &event); err != nil {
			break
		}
		event.ID = eventID
		if len(out.Events) == limit {
			out.More = true
			break
		}
		out.Events = append(out.Events, event)
		out.NextCursor = event.ID
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, fmt.Errorf("read game event history: %w", err)
	}
	return out, tx.Commit()
}

// Run uses one cancellable worker and a fair cursor over at most 32 instances
// each five-second tick. Unexpected database/source failures reach the manager.
func (s *Service) Run(ctx context.Context) error {
	if s.db == nil || s.logs == nil || s.instances == nil {
		return domain.E(domain.CodeUnavailable, "game event ingestion unavailable")
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	last := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM instances WHERE id>? ORDER BY id LIMIT 32`, last)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("list game event instances: %w", err)
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if len(ids) < 32 {
			last = ""
		} else {
			last = ids[len(ids)-1]
		}
		for _, id := range ids {
			if err = s.Poll(ctx, id); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if domain.AsError(err).Code == domain.CodeNotFound {
					continue
				}
				return fmt.Errorf("game event ingestion failed: %w", err)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
