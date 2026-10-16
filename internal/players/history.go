package players

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"regexp"
	"strings"
	"time"
)

type Session struct {
	ID         int64                  `json:"id"`
	Player     console.PlayerIdentity `json:"player"`
	Generation string                 `json:"generation"`
	FirstSeen  time.Time              `json:"first_seen"`
	LastSeen   time.Time              `json:"last_seen"`
	EndedAt    *time.Time             `json:"ended_at,omitempty"`
	Gap        bool                   `json:"gap"`
	EndReason  string                 `json:"end_reason"`
}
type HistoryEvent struct {
	ID           int64                  `json:"id"`
	SourceCursor int64                  `json:"source_cursor"`
	ObservedAt   time.Time              `json:"observed_at"`
	Kind         string                 `json:"kind"`
	Player       console.PlayerIdentity `json:"player"`
	Gap          bool                   `json:"gap"`
}
type HistoryPage struct {
	Sessions        []Session      `json:"sessions"`
	Events          []HistoryEvent `json:"events"`
	NextCursor      int64          `json:"next_cursor"`
	NextEventCursor int64          `json:"next_event_cursor"`
	EventGap        bool           `json:"event_gap"`
	Gap             bool           `json:"gap"`
	Reason          string         `json:"reason"`
}

func key(p console.PlayerIdentity, generation string) string {
	if p.UUID != "" {
		return "uuid:" + p.UUID
	}
	return "name:" + generation + ":" + strings.ToLower(p.Name)
}
func (s *Service) record(ctx context.Context, snapshot console.PlayerSnapshot) error {
	if domain.ValidateInstanceID(snapshot.InstanceID) != nil || snapshot.ObservedAt.IsZero() || len(snapshot.Players) > 10000 {
		return domain.E(domain.CodeValidationFailed, "invalid player observation")
	}
	if snapshot.Known && (!snapshot.Complete || snapshot.Generation == "") {
		return domain.E(domain.CodeValidationFailed, "known player observation requires complete generation-bound roster")
	}
	ids, names := map[string]bool{}, map[string]bool{}
	for _, p := range snapshot.Players {
		if console.ValidatePlayerIdentity(p.UUID, p.Name) != nil || p.UUID != "" && ids[p.UUID] || p.Name != "" && names[strings.ToLower(p.Name)] {
			return domain.E(domain.CodeValidationFailed, "ambiguous player observation")
		}
		ids[p.UUID] = true
		names[strings.ToLower(p.Name)] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_json FROM player_snapshots WHERE instance_id=?`, snapshot.InstanceID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var old console.PlayerSnapshot
	_ = json.Unmarshal([]byte(previous), &old)
	gap := !old.Known || !snapshot.Known || old.Generation != snapshot.Generation || snapshot.ObservedAt.Sub(old.ObservedAt) > 45*time.Second
	if !old.ObservedAt.IsZero() && !snapshot.ObservedAt.After(old.ObservedAt) {
		return tx.Commit()
	}
	now := snapshot.ObservedAt.UTC().Format(time.RFC3339Nano)
	if gap {
		if _, err = tx.ExecContext(ctx, `UPDATE player_sessions SET ended_at=?,gap=1,end_reason='observation_gap' WHERE instance_id=? AND ended_at IS NULL`, now, snapshot.InstanceID); err != nil {
			return err
		}
	}
	present := map[string]bool{}
	if snapshot.Known {
		for _, p := range snapshot.Players {
			k := key(p, snapshot.Generation)
			present[k] = true
			b, _ := json.Marshal(p)
			res, e := tx.ExecContext(ctx, `UPDATE player_sessions SET last_seen=?,identity_json=? WHERE instance_id=? AND identity_key=? AND ended_at IS NULL`, now, string(b), snapshot.InstanceID, k)
			if e != nil {
				return e
			}
			n, e := res.RowsAffected()
			if e != nil {
				return e
			}
			if n == 0 {
				if _, e = tx.ExecContext(ctx, `INSERT INTO player_sessions(instance_id,identity_key,identity_json,generation,first_seen,last_seen,gap) VALUES(?,?,?,?,?,?,?)`, snapshot.InstanceID, k, string(b), snapshot.Generation, now, now, gap); e != nil {
					return e
				}
			}
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,identity_key FROM player_sessions WHERE instance_id=? AND ended_at IS NULL`, snapshot.InstanceID)
	if err != nil {
		return err
	}
	absent := []int64{}
	for rows.Next() {
		var id int64
		var k string
		if err = rows.Scan(&id, &k); err != nil {
			break
		}
		if !present[k] {
			absent = append(absent, id)
		}
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, id := range absent {
		if _, err = tx.ExecContext(ctx, `UPDATE player_sessions SET ended_at=?,end_reason='absent_at_observation' WHERE id=?`, now, id); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(snapshot)
	if _, err = tx.ExecContext(ctx, `INSERT INTO player_snapshots(instance_id,snapshot_json) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET snapshot_json=excluded.snapshot_json`, snapshot.InstanceID, string(b)); err != nil {
		return err
	}
	cutoff := snapshot.ObservedAt.Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	var floor int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM player_sessions WHERE instance_id=? AND (last_seen<? OR id NOT IN (SELECT id FROM player_sessions WHERE instance_id=? ORDER BY id DESC LIMIT 10000))`, snapshot.InstanceID, cutoff, snapshot.InstanceID).Scan(&floor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM player_sessions WHERE instance_id=? AND (last_seen<? OR id NOT IN (SELECT id FROM player_sessions WHERE instance_id=? ORDER BY id DESC LIMIT 10000))`, snapshot.InstanceID, cutoff, snapshot.InstanceID); err != nil {
		return err
	}
	if floor > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE player_snapshots SET history_floor=MAX(history_floor,?) WHERE instance_id=?`, floor, snapshot.InstanceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Log timestamps are ingestion observation times. We do not manufacture dates
// from HH:MM:SS console prefixes or join UUIDs to names from another event.
var playerEvent = regexp.MustCompile(`^\[[0-9]{2}:[0-9]{2}:[0-9]{2}\] \[Server thread/INFO\]: ([A-Za-z0-9_]{1,16}) (joined|left) the game$`)

func (s *Service) consumeLogs(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cursor int64
	if err := s.db.QueryRowContext(ctx, `SELECT log_cursor FROM player_snapshots WHERE instance_id=?`, id).Scan(&cursor); err != nil {
		return err
	}
	status, statusErr := s.console.Status(ctx, id)
	sourceGap := statusErr != nil || !status.LogsAvailable
	if s.instances != nil {
		detail, e := s.instances.Get(ctx, id)
		if e != nil {
			return e
		}
		if detail.Instance.MinecraftVersion == "26.3" || strings.HasPrefix(detail.Instance.MinecraftVersion, "26.3.") {
			sourceGap = true
		}
	}
	page, err := s.console.History(ctx, id, cursor, 500)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, line := range page.Lines {
		m := playerEvent.FindStringSubmatch(line.Message)
		if len(m) == 0 && !line.Gap {
			continue
		}
		kind := "source_gap"
		p := console.PlayerIdentity{Authority: "unknown", Source: "redacted_native_log"}
		if len(m) > 0 {
			kind = "join_observed"
			if m[2] == "left" {
				kind = "leave_observed"
			}
			p.Name = m[1]
			p.Authority = "name_only"
		}
		b, _ := json.Marshal(p)
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO player_history_events(instance_id,source_cursor,observed_at,kind,identity_json,gap) VALUES(?,?,?,?,?,?)`, id, line.ID, line.CreatedAt.UTC().Format(time.RFC3339Nano), kind, string(b), line.Gap || page.Gap); err != nil {
			return err
		}
	}
	if page.Gap || sourceGap {
		if _, err = tx.ExecContext(ctx, `UPDATE player_snapshots SET event_floor=MAX(event_floor,1) WHERE instance_id=?`, id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE player_snapshots SET log_cursor=? WHERE instance_id=?`, page.NextCursor, id); err != nil {
		return err
	}
	var eventFloor int64
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM player_history_events WHERE instance_id=? AND (observed_at<? OR id NOT IN (SELECT id FROM player_history_events WHERE instance_id=? ORDER BY id DESC LIMIT 20000))`, id, cutoff, id).Scan(&eventFloor); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE player_snapshots SET event_floor=MAX(event_floor,?) WHERE instance_id=?`, eventFloor, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM player_history_events WHERE instance_id=? AND (observed_at<? OR id NOT IN (SELECT id FROM player_history_events WHERE instance_id=? ORDER BY id DESC LIMIT 20000))`, id, time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) History(ctx context.Context, id string, after int64, limit int) (HistoryPage, error) {
	return s.HistoryWithEvents(ctx, id, after, 0, limit)
}
func (s *Service) HistoryWithEvents(ctx context.Context, id string, after, eventAfter int64, limit int) (HistoryPage, error) {
	out := HistoryPage{Sessions: []Session{}, Events: []HistoryEvent{}, Reason: "Session times are sampled observation boundaries; short sessions and downtime may be missing. Log times are ingestion observations; name-only events never prove UUIDs. Native join/leave logs can be absent, including in 26.3; missing events never prove no joins or leaves. History retains at most 30 days and 10000 sessions plus 20000 log events per instance."}
	if domain.ValidateInstanceID(id) != nil || after < 0 || eventAfter < 0 || limit < 1 || limit > 500 {
		return out, domain.E(domain.CodeValidationFailed, "invalid player history cursor or limit")
	}
	if s.instances != nil {
		if _, err := s.instances.Get(ctx, id); err != nil {
			return out, err
		}
	}
	var floor, maxID int64
	err := s.db.QueryRowContext(ctx, `SELECT history_floor FROM player_snapshots WHERE instance_id=?`, id).Scan(&floor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM player_sessions WHERE instance_id=?`, id).Scan(&maxID); err != nil {
		return out, err
	}
	var owned int
	if after > 0 {
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_sessions WHERE instance_id=? AND id=?`, id, after).Scan(&owned); err != nil {
			return out, err
		}
	}
	if after > maxID || after > 0 && owned == 0 {
		after = 0
		out.Gap = true
	}
	out.Gap = out.Gap || floor > 0
	out.NextCursor = after
	rows, err := s.db.QueryContext(ctx, `SELECT id,identity_json,generation,first_seen,last_seen,ended_at,gap,end_reason FROM player_sessions WHERE instance_id=? AND id>? ORDER BY id LIMIT ?`, id, after, limit)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v Session
		var raw, first, last string
		var ended sql.NullString
		if err = rows.Scan(&v.ID, &raw, &v.Generation, &first, &last, &ended, &v.Gap, &v.EndReason); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &v.Player); err != nil {
			break
		}
		v.FirstSeen, err = time.Parse(time.RFC3339Nano, first)
		if err != nil {
			break
		}
		v.LastSeen, err = time.Parse(time.RFC3339Nano, last)
		if err != nil {
			break
		}
		if ended.Valid {
			at, e := time.Parse(time.RFC3339Nano, ended.String)
			if e != nil {
				err = e
				break
			}
			v.EndedAt = &at
		}
		out.Sessions = append(out.Sessions, v)
		out.NextCursor = v.ID
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return out, fmt.Errorf("read player sessions: %w", err)
	}
	// Event and session cursors have independent ordering and retention floors.
	var eventFloor, eventMax int64
	if err = s.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT event_floor FROM player_snapshots WHERE instance_id=?),0),(SELECT COALESCE(MAX(id),0) FROM player_history_events WHERE instance_id=?)`, id, id).Scan(&eventFloor, &eventMax); err != nil {
		return out, err
	}
	out.EventGap = eventFloor > 0
	owned = 0
	if eventAfter > 0 {
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM player_history_events WHERE instance_id=? AND id=?`, id, eventAfter).Scan(&owned); err != nil {
			return out, err
		}
	}
	if eventAfter > eventMax || eventAfter > 0 && owned == 0 {
		eventAfter = 0
		out.EventGap = true
	}
	out.NextEventCursor = eventAfter
	rows, err = s.db.QueryContext(ctx, `SELECT id,source_cursor,observed_at,kind,identity_json,gap FROM player_history_events WHERE instance_id=? AND id>? ORDER BY id LIMIT ?`, id, eventAfter, limit)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var event HistoryEvent
		var ts, raw string
		if err = rows.Scan(&event.ID, &event.SourceCursor, &ts, &event.Kind, &raw, &event.Gap); err != nil {
			return out, err
		}
		if err = json.Unmarshal([]byte(raw), &event.Player); err != nil {
			return out, err
		}
		event.ObservedAt, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return out, err
		}
		out.Events = append(out.Events, event)
		out.NextEventCursor = event.ID
	}
	return out, rows.Err()
}
