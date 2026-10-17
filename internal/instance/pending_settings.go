package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

const maxPendingRevision int64 = 9007199254740991

type PendingSettingsDraft struct {
	Name      string                `json:"name"`
	MemoryMiB int                   `json:"memory_mib"`
	Config    domain.InstanceConfig `json:"config"`
}
type PendingSettings struct {
	Revision      int64                 `json:"revision"`
	Draft         *PendingSettingsDraft `json:"draft"`
	BaseUpdatedAt string                `json:"base_updated_at"`
	UpdatedAt     string                `json:"updated_at"`
	UpdatedBy     int64                 `json:"updated_by"`
	Stale         bool                  `json:"stale"`
}
type SavePendingSettings struct {
	PendingSettingsDraft
	ExpectedRevision          int64  `json:"expected_revision"`
	ExpectedInstanceUpdatedAt string `json:"expected_instance_updated_at"`
}
type PendingSettingsGuard interface {
	WithPendingSettings(context.Context, string, func() error) error
}

func WithPendingSettingsGuard(g PendingSettingsGuard) Option {
	return func(s *Service) { s.pendingGuard = g }
}

type pendingReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func freshPendingAdmin(ctx context.Context, r pendingReader, id int64) (domain.User, error) {
	fresh := domain.User{ID: id}
	err := r.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, id).Scan(&fresh.Username, &fresh.Role, &fresh.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return fresh, domain.E(domain.CodeForbidden, "pending settings require a fresh enabled administrator; resave or discard the draft")
	}
	if err != nil {
		return fresh, fmt.Errorf("read pending settings author: %w", err)
	}
	if err = adminOnly(fresh); err != nil {
		return fresh, domain.E(domain.CodeForbidden, "pending settings require a fresh enabled administrator; resave or discard the draft")
	}
	return fresh, nil
}
func pendingDraft(d Detail, p PendingSettingsDraft) (Draft, error) {
	d.Instance.Name = p.Name
	d.Instance.MemoryMiB = p.MemoryMiB
	// Runtime identity belongs to the installed selection, never the desired draft.
	d.Instance.JavaRuntimeID = ""
	next := Draft{Instance: d.Instance, Config: p.Config}
	if err := validateDraft(next); err != nil {
		return Draft{}, err
	}
	if next.Config.LevelName != d.Config.LevelName {
		return Draft{}, domain.E(domain.CodeConflict, "world directory cannot be changed by pending settings")
	}
	return next, nil
}
func readPendingSettings(ctx context.Context, r pendingReader, id string, d Detail) (PendingSettings, error) {
	var p PendingSettings
	var raw sql.NullString
	var current string
	if err := r.QueryRowContext(ctx, `SELECT updated_at FROM instances WHERE id=?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return p, domain.NotFound("instance")
		}
		return p, err
	}
	err := r.QueryRowContext(ctx, `SELECT revision,draft_json,base_updated_at,updated_at,updated_by FROM instance_pending_settings WHERE instance_id=?`, id).Scan(&p.Revision, &raw, &p.BaseUpdatedAt, &p.UpdatedAt, &p.UpdatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingSettings{}, nil
	}
	if err != nil {
		return p, fmt.Errorf("read pending settings: %w", err)
	}
	invalid := func() (PendingSettings, error) {
		return PendingSettings{}, domain.E(domain.CodeUnavailable, "stored pending settings are invalid; administrator repair required")
	}
	if p.Revision < 1 || p.Revision > maxPendingRevision || p.UpdatedBy <= 0 {
		return invalid()
	}
	if _, err = time.Parse(time.RFC3339Nano, p.BaseUpdatedAt); err != nil {
		return invalid()
	}
	if _, err = time.Parse(time.RFC3339Nano, p.UpdatedAt); err != nil {
		return invalid()
	}
	if raw.Valid {
		if len(raw.String) > 2<<20 {
			return invalid()
		}

		var fields map[string]json.RawMessage
		if err = json.Unmarshal([]byte(raw.String), &fields); err != nil || !pendingFieldsPresent(fields, "name", "memory_mib", "config") {
			return invalid()
		}
		var configFields map[string]json.RawMessage
		if err = json.Unmarshal(fields["config"], &configFields); err != nil || !pendingFieldsPresent(configFields, "motd", "difficulty", "game_mode", "max_players", "online_mode", "pvp", "view_distance", "simulation_distance", "level_name", "level_seed", "extra") {
			return invalid()
		}
		decoder := json.NewDecoder(strings.NewReader(raw.String))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&p.Draft); err != nil || p.Draft == nil {
			return invalid()
		}
		if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
			return invalid()
		}
		if p.Draft.Config.Extra == nil {
			p.Draft.Config.Extra = map[string]string{}
		}
		if _, err = pendingDraft(d, *p.Draft); err != nil {
			return invalid()
		}
		p.Stale = p.BaseUpdatedAt != current
	}
	return p, nil
}
func (s *Service) GetPendingSettings(ctx context.Context, id string) (PendingSettings, error) {
	if err := validID(id); err != nil {
		return PendingSettings{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.Get(ctx, id)
	if err != nil {
		return PendingSettings{}, err
	}
	return readPendingSettings(ctx, s.db, id, d)
}
func (s *Service) withPendingSettings(ctx context.Context, id string, fn func() error) error {
	if err := validID(id); err != nil {
		return err
	}
	if s.pendingGuard == nil {
		return domain.E(domain.CodeUnavailable, "pending settings admission is unavailable")
	}
	return s.pendingGuard.WithPendingSettings(ctx, id, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	})
}
func (s *Service) SavePendingSettings(ctx context.Context, actor domain.User, id string, in SavePendingSettings) (PendingSettings, error) {
	if err := adminOnly(actor); err != nil {
		return PendingSettings{}, err
	}
	if in.ExpectedRevision < 0 || in.ExpectedRevision > maxPendingRevision || in.ExpectedInstanceUpdatedAt == "" {
		return PendingSettings{}, domain.E(domain.CodeValidationFailed, "current pending revision and instance revision are required")
	}
	var out PendingSettings
	err := s.withPendingSettings(ctx, id, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		fresh, err := freshPendingAdmin(ctx, tx, actor.ID)
		if err != nil {
			return err
		}
		d, err := scanDetail(tx.QueryRowContext(ctx, selectDetail+` WHERE i.id=?`, id))
		if err != nil {
			return err
		}
		var revision string
		if err = tx.QueryRowContext(ctx, `SELECT updated_at FROM instances WHERE id=?`, id).Scan(&revision); err != nil {
			return err
		}
		if revision != in.ExpectedInstanceUpdatedAt {
			return domain.E(domain.CodeConflict, "active settings changed; reload and deliberately rebase or discard pending settings")
		}
		if in.Config.Extra == nil {
			in.Config.Extra = d.Config.Extra
		}
		if _, err = pendingDraft(d, in.PendingSettingsDraft); err != nil {
			return err
		}
		before, err := readPendingSettings(ctx, tx, id, d)
		if err != nil {
			return err
		}
		if before.Draft != nil && before.UpdatedBy == fresh.ID && !before.Stale && before.BaseUpdatedAt == revision && reflect.DeepEqual(*before.Draft, in.PendingSettingsDraft) {
			out = before
			return nil
		}
		if before.Revision != in.ExpectedRevision {
			return domain.E(domain.CodeConflict, "pending settings changed; reload before saving")
		}
		if before.Revision >= maxPendingRevision {
			return domain.E(domain.CodeUnavailable, "pending settings revision exhausted")
		}
		raw, err := json.Marshal(in.PendingSettingsDraft)
		if err != nil {
			return err
		}
		out = PendingSettings{Revision: before.Revision + 1, Draft: &in.PendingSettingsDraft, BaseUpdatedAt: revision, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedBy: fresh.ID}
		if _, err = tx.ExecContext(ctx, `INSERT INTO instance_pending_settings(instance_id,revision,draft_json,base_updated_at,updated_at,updated_by) VALUES(?,?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET revision=excluded.revision,draft_json=excluded.draft_json,base_updated_at=excluded.base_updated_at,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, id, out.Revision, string(raw), out.BaseUpdatedAt, out.UpdatedAt, out.UpdatedBy); err != nil {
			return fmt.Errorf("save pending settings: %w", err)
		}
		if err = audit(ctx, tx, fresh, id, "instance.pending_settings.save"); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return PendingSettings{}, err
	}
	return out, nil
}
func (s *Service) DiscardPendingSettings(ctx context.Context, actor domain.User, id string, expected int64) (PendingSettings, error) {
	if err := adminOnly(actor); err != nil {
		return PendingSettings{}, err
	}
	if expected < 0 || expected > maxPendingRevision {
		return PendingSettings{}, domain.E(domain.CodeValidationFailed, "pending revision must be nonnegative")
	}
	var out PendingSettings
	err := s.withPendingSettings(ctx, id, func() error {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		fresh, err := freshPendingAdmin(ctx, tx, actor.ID)
		if err != nil {
			return err
		}
		d, err := scanDetail(tx.QueryRowContext(ctx, selectDetail+` WHERE i.id=?`, id))
		if err != nil {
			return err
		}
		before, err := readPendingSettings(ctx, tx, id, d)
		if err != nil {
			return err
		}
		if before.Revision != expected {
			return domain.E(domain.CodeConflict, "pending settings changed; reload before discarding")
		}
		out = before
		if before.Draft == nil {
			return nil
		}
		if before.Revision >= maxPendingRevision {
			return domain.E(domain.CodeUnavailable, "pending settings revision exhausted")
		}
		out.Revision++
		out.Draft = nil
		out.Stale = false
		out.UpdatedBy = fresh.ID
		out.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = tx.ExecContext(ctx, `UPDATE instance_pending_settings SET revision=?,draft_json=NULL,updated_at=?,updated_by=? WHERE instance_id=?`, out.Revision, out.UpdatedAt, out.UpdatedBy, id); err != nil {
			return err
		}
		if err = audit(ctx, tx, fresh, id, "instance.pending_settings.discard"); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return PendingSettings{}, err
	}
	return out, nil
}

// ApplyPendingSettings is valid only on the original stopped session and original
// current execution. It never reacquires queue or native admission. Durable job
// identity and payload, not mutable public execution fields, grant this capability.
func (session *StoppedSession) ApplyPendingSettings(ctx context.Context, e *jobs.Execution) error {
	scope, release, err := session.enter(ctx)
	if err != nil {
		return err
	}
	defer release()
	if scope.execution == nil || scope.execution != e {
		return domain.E(domain.CodeForbidden, "pending settings require the original stopped job session")
	}
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	current, err := e.CheckCurrent(operation)
	if err != nil {
		return err
	}
	if current.InstanceID != scope.id {
		return domain.E(domain.CodeForbidden, "pending settings job instance differs from stopped session")
	}
	s := scope.service
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = checkPendingJob(operation, s.db, current); err != nil {
		return err
	}
	previous, err := s.Get(operation, scope.id)
	if err != nil {
		return err
	}
	pending, err := readPendingSettings(operation, s.db, scope.id, previous)
	if err != nil {
		return err
	}
	if pending.Draft == nil {
		return nil
	}
	if pending.Stale {
		return domain.E(domain.CodeConflict, "pending settings are stale; administrator must rebase/save or discard before start")
	}
	author, err := freshPendingAdmin(operation, s.db, pending.UpdatedBy)
	if err != nil {
		return err
	}
	next, err := pendingDraft(previous, *pending.Draft)
	if err != nil {
		return err
	}
	_, err = s.updateStopped(operation, author, scope.id, next, &pendingApply{current: current, revision: pending.Revision, base: pending.BaseUpdatedAt})
	return err
}

type pendingApply struct {
	current  jobs.CurrentExecution
	revision int64
	base     string
}

func checkPendingJob(ctx context.Context, r pendingReader, current jobs.CurrentExecution) error {
	var kind, payload string
	var requester int64
	err := r.QueryRowContext(ctx, `SELECT kind,payload,requested_by FROM jobs WHERE id=? AND instance_id=? AND state='running'`, current.JobID, current.InstanceID).Scan(&kind, &payload, &requester)
	if err != nil {
		return domain.E(domain.CodeForbidden, "pending settings require the current durable running job")
	}
	if kind != current.Kind || requester != current.RequesterID {
		return domain.E(domain.CodeForbidden, "pending settings job identity changed")
	}
	if kind == "crash_recovery" {
		return nil
	}
	if kind == "lifecycle" {
		var in struct {
			Operation string `json:"operation"`
		}
		if json.Unmarshal([]byte(payload), &in) == nil && (in.Operation == "start" || in.Operation == "restart") {
			return nil
		}
	}
	return domain.E(domain.CodeForbidden, "only ordinary managed start/restart or crash recovery may apply pending settings")
}

func (s *Service) getPendingSettingsUnlocked(ctx context.Context, id string) (PendingSettings, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return PendingSettings{}, err
	}
	return readPendingSettings(ctx, s.db, id, d)
}

func pendingFieldsPresent(fields map[string]json.RawMessage, required ...string) bool {
	for _, name := range required {
		value, ok := fields[name]
		if !ok || string(value) == "null" {
			return false
		}
	}
	return true
}
