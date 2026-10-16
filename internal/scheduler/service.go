package scheduler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

type CommandProducer interface {
	EnqueueCommandOnce(context.Context, string, string, string, domain.User) (jobs.Job, error)
}
type BackupProducer interface {
	EnqueueAutomaticOnce(context.Context, string, string, domain.BackupKind, domain.User) (jobs.Job, error)
}
type RestartProducer interface {
	EnqueueRestartOnce(context.Context, string, string, int, domain.User) (jobs.Job, error)
}

// AutomaticUpdateProducer may only derive targets from the CURRENT pinned
// Minecraft version; its implementation owns backup/conflict/compatibility gates.
type AutomaticUpdateProducer interface {
	EnqueueAutomaticOnce(context.Context, string, string, domain.User) (jobs.Job, error)
}
type RecoveryNative interface {
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
}
type RecoveryLifecycle interface {
	StartForRecovery(context.Context, *jobs.Execution, domain.User, lifecycle.RecoveryStartGuard) error
}
type CrashEvent struct {
	ID         string
	InstanceID string
	OccurredAt time.Time
}
type Config struct {
	Log          *slog.Logger
	Native       RecoveryNative
	Lifecycle    RecoveryLifecycle
	Jobs         *jobs.Service
	OnCrash      func(context.Context, CrashEvent) error
	DB           *sql.DB
	HostTimezone string
	Now          func() time.Time
	Commands     CommandProducer
	Backups      BackupProducer
	Restarts     RestartProducer
	Updates      AutomaticUpdateProducer
}
type Service struct {
	recoveryLocks   sync.Map
	recoveryCancels sync.Map
	config          Config
	db              *sql.DB
	now             func() time.Time
	host            string
}

func New(c Config) *Service {
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.HostTimezone == "" {
		c.HostTimezone = HostTimezone()
	}
	return &Service{config: c, db: c.DB, now: c.Now, host: c.HostTimezone}
}
func (s *Service) DefaultTimezone() string { return s.host }

type Record struct {
	domain.Schedule
	ActorID        int64  `json:"actor_id"`
	DisabledReason string `json:"disabled_reason"`
	LastRun        *Run   `json:"last_run,omitempty"`
}
type Run struct {
	ID         string          `json:"id"`
	ScheduleID string          `json:"schedule_id"`
	InstanceID string          `json:"instance_id"`
	DueAt      time.Time       `json:"due_at"`
	State      string          `json:"state"`
	JobID      string          `json:"job_id,omitempty"`
	JobState   domain.JobState `json:"job_state,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

var scheduleKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func key() (string, error) {
	var raw [16]byte
	_, e := rand.Read(raw[:])
	return hex.EncodeToString(raw[:]), e
}
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func (s *Service) actor(ctx context.Context, id int64) (domain.User, error) {
	u := domain.User{ID: id}
	if e := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, id).Scan(&u.Username, &u.Role, &u.Disabled); e != nil {
		return u, domain.E(domain.CodeForbidden, "schedule owner unavailable")
	}
	return u, authorized(u)
}
func (s *Service) selection(ctx context.Context, id string) (domain.VersionSelection, error) {
	var out domain.VersionSelection
	if e := domain.ValidateInstanceID(id); e != nil {
		return out, e
	}
	e := s.db.QueryRowContext(ctx, `SELECT flavor,minecraft_version,loader_version FROM instances WHERE id=?`, id).Scan(&out.Flavor, &out.MinecraftVersion, &out.LoaderVersion)
	if e == sql.ErrNoRows {
		return out, domain.NotFound("instance")
	}
	return out, e
}
func (s *Service) available(action domain.ScheduleAction) bool {
	switch action {
	case domain.ScheduleBackup:
		return s.config.Backups != nil
	case domain.ScheduleCommand, domain.ScheduleAnnouncement:
		return s.config.Commands != nil
	case domain.ScheduleRestart:
		return s.config.Restarts != nil
	case domain.ScheduleUpdate:
		return s.config.Updates != nil
	}
	return false
}
func (s *Service) Save(ctx context.Context, instanceID, id string, in Input, actor domain.User) (Record, error) {
	var out Record
	if e := authorized(actor); e != nil {
		return out, e
	}
	fresh, e := s.actor(ctx, actor.ID)
	if e != nil {
		return out, e
	}
	selection, e := s.selection(ctx, instanceID)
	if e != nil {
		return out, e
	}
	definition, e := validate(in, selection, fresh, s.host, s.now())
	if e != nil {
		return out, e
	}
	if definition.Enabled && !s.available(definition.Action) {
		return out, domain.E(domain.CodeUnavailable, "schedule action is unavailable; keep the definition disabled until its producer is configured")
	}
	create := id == ""
	if create {
		id, e = key()
		if e != nil {
			return out, e
		}
	} else if !scheduleKey.MatchString(id) {
		return out, domain.E(domain.CodeValidationFailed, "invalid schedule identifier")
	}
	definition.ID = id
	definition.InstanceID = instanceID
	out = Record{Schedule: definition, ActorID: fresh.ID}
	raw, e := json.Marshal(definition)
	if e != nil {
		return out, e
	}
	next := ""
	if definition.NextRunAt != nil {
		next = stamp(*definition.NextRunAt)
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if create {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM schedules WHERE instance_id=?`, instanceID).Scan(&count); e != nil {
			return out, e
		}
		if count >= 100 {
			return out, domain.E(domain.CodeConflict, "instance already has 100 schedules")
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO schedules(id,instance_id,actor_id,definition_json,enabled,next_run_at,updated_at) VALUES(?,?,?,?,?,?,?)`, id, instanceID, fresh.ID, string(raw), definition.Enabled, next, stamp(s.now()))
	} else {
		var owner int64
		e = tx.QueryRowContext(ctx, `SELECT actor_id FROM schedules WHERE id=? AND instance_id=?`, id, instanceID).Scan(&owner)
		if e == sql.ErrNoRows {
			return out, domain.NotFound("schedule")
		}
		if e != nil {
			return out, e
		}
		if owner != fresh.ID && fresh.Role != domain.RoleAdmin {
			return out, domain.E(domain.CodeForbidden, "only the schedule owner or an administrator may edit it")
		}
		_, e = tx.ExecContext(ctx, `UPDATE schedules SET actor_id=?,definition_json=?,enabled=?,next_run_at=?,disabled_reason='',updated_at=? WHERE id=? AND instance_id=?`, fresh.ID, string(raw), definition.Enabled, next, stamp(s.now()), id, instanceID)
	}
	if e != nil {
		return out, fmt.Errorf("save schedule: %w", e)
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target) VALUES(?,?,?,'schedule.save',?,?)`, stamp(s.now()), fresh.ID, fresh.Username, instanceID, id); e != nil {
		return out, e
	}
	if e = tx.Commit(); e != nil {
		return out, e
	}
	return out, nil
}
func (s *Service) Delete(ctx context.Context, instanceID, id string, actor domain.User) error {
	if e := authorized(actor); e != nil {
		return e
	}
	fresh, e := s.actor(ctx, actor.ID)
	if e != nil {
		return e
	}
	if domain.ValidateInstanceID(instanceID) != nil || !scheduleKey.MatchString(id) {
		return domain.E(domain.CodeValidationFailed, "invalid schedule identifier")
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var owner int64
	e = tx.QueryRowContext(ctx, `SELECT actor_id FROM schedules WHERE instance_id=? AND id=?`, instanceID, id).Scan(&owner)
	if e == sql.ErrNoRows {
		return domain.NotFound("schedule")
	}
	if e != nil {
		return e
	}
	if owner != fresh.ID && fresh.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "only the schedule owner or an administrator may delete it")
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM schedules WHERE instance_id=? AND id=?`, instanceID, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target) VALUES(?,?,?,'schedule.delete',?,?)`, stamp(s.now()), fresh.ID, fresh.Username, instanceID, id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Service) List(ctx context.Context, id string) ([]Record, error) {
	if _, e := s.selection(ctx, id); e != nil {
		return nil, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT definition_json,actor_id,enabled,next_run_at,disabled_reason FROM schedules WHERE instance_id=? ORDER BY id`, id)
	if e != nil {
		return nil, e
	}
	out := []Record{}
	for rows.Next() {
		var r Record
		var raw, next string
		if e = rows.Scan(&raw, &r.ActorID, &r.Enabled, &next, &r.DisabledReason); e != nil {
			rows.Close()
			return nil, e
		}
		enabled := r.Enabled
		if e = json.Unmarshal([]byte(raw), &r.Schedule); e != nil {
			rows.Close()
			return nil, e
		}
		r.Enabled = enabled
		r.NextRunAt = nil
		if next != "" {
			t, e := time.Parse(time.RFC3339Nano, next)
			if e != nil {
				rows.Close()
				return nil, e
			}
			r.NextRunAt = &t
		}
		out = append(out, r)
	}
	e = errors.Join(rows.Err(), rows.Close())
	if e != nil {
		return nil, e
	}
	for i := range out {
		runs, e := s.History(ctx, id, out[i].ID)
		if e != nil {
			return nil, e
		}
		if len(runs) > 0 {
			out[i].LastRun = &runs[0]
		}
	}
	return out, nil
}
func (s *Service) History(ctx context.Context, id, schedule string) ([]Run, error) {
	if domain.ValidateInstanceID(id) != nil || !scheduleKey.MatchString(schedule) {
		return nil, domain.E(domain.CodeValidationFailed, "invalid schedule identifier")
	}
	rows, e := s.db.QueryContext(ctx, `SELECT r.id,r.schedule_id,r.instance_id,r.due_at,r.state,r.job_id,r.reason,COALESCE(j.state,'') FROM schedule_runs r LEFT JOIN jobs j ON j.id=r.job_id WHERE r.instance_id=? AND r.schedule_id=? ORDER BY r.due_at DESC LIMIT 100`, id, schedule)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		var ts string
		if e = rows.Scan(&r.ID, &r.ScheduleID, &r.InstanceID, &ts, &r.State, &r.JobID, &r.Reason, &r.JobState); e != nil {
			return nil, e
		}
		r.DueAt, e = time.Parse(time.RFC3339Nano, ts)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) AvailableActions() []domain.ScheduleAction {
	out := []domain.ScheduleAction{}
	for _, a := range []domain.ScheduleAction{domain.ScheduleBackup, domain.ScheduleCommand, domain.ScheduleAnnouncement, domain.ScheduleRestart, domain.ScheduleUpdate} {
		if s.available(a) {
			out = append(out, a)
		}
	}
	return out
}
