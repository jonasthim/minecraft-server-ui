package scheduler

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type RecoveryStatus struct {
	Enabled  bool      `json:"enabled"`
	ActorID  int64     `json:"actor_id"`
	Reason   string    `json:"reason"`
	Attempts []Attempt `json:"attempts"`
}
type Attempt struct {
	ID         string     `json:"id"`
	DueAt      time.Time  `json:"due_at"`
	AdmittedAt *time.Time `json:"admitted_at,omitempty"`
	State      string     `json:"state"`
	JobID      string     `json:"job_id,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

func (s *Service) recoveryLock(id string) *sync.Mutex {
	m, _ := s.recoveryLocks.LoadOrStore(id, &sync.Mutex{})
	return m.(*sync.Mutex)
}
func generation(identity domain.ProcessIdentity) string {
	raw, _ := json.Marshal(identity)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func proof(identity domain.ProcessIdentity, observed supervisor.Observation, now time.Time) bool {
	return identity.PID > 0 && identity.StartIdentity != "" && !identity.StartedAt.IsZero() && !observed.ProcessRunning && observed.Exit != nil && observed.Exit.Identity == identity && !observed.Exit.Deliberate && !observed.Exit.ExitedAt.IsZero() && !observed.Exit.ExitedAt.Before(identity.StartedAt) && !observed.Exit.ExitedAt.After(now)
}
func (s *Service) Recovery(ctx context.Context, id string) (RecoveryStatus, error) {
	out := RecoveryStatus{Attempts: []Attempt{}}
	if _, e := s.selection(ctx, id); e != nil {
		return out, e
	}
	e := s.db.QueryRowContext(ctx, `SELECT enabled,actor_id,reason FROM crash_recovery WHERE instance_id=?`, id).Scan(&out.Enabled, &out.ActorID, &out.Reason)
	if e != nil && e != sql.ErrNoRows {
		return out, e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,due_at,admitted_at,state,job_id,reason FROM crash_attempts WHERE instance_id=? ORDER BY due_at DESC,id LIMIT 100`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var a Attempt
		var due, admitted string
		if e = rows.Scan(&a.ID, &due, &admitted, &a.State, &a.JobID, &a.Reason); e != nil {
			return out, e
		}
		a.DueAt, e = time.Parse(time.RFC3339Nano, due)
		if e != nil {
			return out, e
		}
		if admitted != "" {
			t, e := time.Parse(time.RFC3339Nano, admitted)
			if e != nil {
				return out, e
			}
			a.AdmittedAt = &t
		}
		out.Attempts = append(out.Attempts, a)
	}
	return out, rows.Err()
}
func (s *Service) ConfigureRecovery(ctx context.Context, id string, enabled bool, actor domain.User) (RecoveryStatus, error) {
	if actor.Disabled || actor.Role != domain.RoleAdmin {
		return RecoveryStatus{}, domain.E(domain.CodeForbidden, "administrator access required")
	}
	fresh, e := s.actor(ctx, actor.ID)
	if e != nil {
		return RecoveryStatus{}, e
	}
	if fresh.Role != domain.RoleAdmin {
		return RecoveryStatus{}, domain.E(domain.CodeForbidden, "administrator access required")
	}
	if _, e = s.selection(ctx, id); e != nil {
		return RecoveryStatus{}, e
	}
	lock := s.recoveryLock(id)
	lock.Lock()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		lock.Unlock()
		return RecoveryStatus{}, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `INSERT INTO crash_recovery(instance_id,enabled,actor_id,epoch,updated_at) VALUES(?,?,?,1,?) ON CONFLICT(instance_id) DO UPDATE SET enabled=excluded.enabled,actor_id=excluded.actor_id,epoch=crash_recovery.epoch+1,reason='',updated_at=excluded.updated_at`, id, enabled, fresh.ID, stamp(s.now()))
	if e == nil {
		_, e = tx.ExecContext(ctx, `UPDATE crash_attempts SET state='cancelled',reason='Recovery configuration changed.' WHERE instance_id=? AND state IN('pending','queued')`, id)
	}
	if e == nil {
		_, e = tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target) VALUES(?,?,?,'recovery.configure',?,?)`, stamp(s.now()), fresh.ID, fresh.Username, id, id)
	}
	if e == nil {
		e = tx.Commit()
	}
	lock.Unlock()
	if e != nil {
		return RecoveryStatus{}, e
	}
	return s.Recovery(ctx, id)
}

// CancelRecovery is the lifecycle deliberate-stop hook. Its epoch and exact
// suppressed generation survive manager restart, including a stop with no live
// process. A later manual start has a different exact native generation.
func (s *Service) CancelRecovery(ctx context.Context, id string, actor domain.User) error {
	if e := authorized(actor); e != nil {
		return e
	}
	if _, e := s.actor(ctx, actor.ID); e != nil {
		return e
	}
	if domain.ValidateInstanceID(id) != nil {
		return domain.E(domain.CodeValidationFailed, "invalid instance identifier")
	}
	lock := s.recoveryLock(id)
	lock.Lock()
	defer lock.Unlock()
	_, err := s.cancelRecoveryLocked(ctx, id, actor)
	return err
}

// cancelRecoveryLocked preserves deliberate-stop semantics and returns exactly
// the epoch committed under the caller-held per-instance recovery mutex.
func (s *Service) cancelRecoveryLocked(ctx context.Context, id string, actor domain.User) (int64, error) {
	suppressed := ""
	if s.config.Native != nil {
		if identity, e := s.config.Native.Identity(ctx, id); e == nil && identity.InstanceID == id && identity.PID > 0 && identity.StartIdentity != "" && !identity.StartedAt.IsZero() {
			suppressed = generation(identity)
		}
	}
	// An uncertain identity cannot provide an exact generation to suppress.
	// Persist suspension in the same transaction as the stop epoch so that a
	// restored observation or manager restart cannot revive the stopped intent.
	reason := "Deliberate operator stop cancels crash recovery."
	if suppressed == "" {
		reason = "Deliberate stop could not verify native identity; crash recovery is suspended. An administrator must review and explicitly re-enable it."
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return 0, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(ctx, `INSERT INTO crash_recovery(instance_id,enabled,actor_id,epoch,last_generation,reason,updated_at) VALUES(?,0,?,1,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET enabled=CASE WHEN excluded.last_generation='' THEN 0 ELSE crash_recovery.enabled END,epoch=crash_recovery.epoch+1,last_generation=excluded.last_generation,reason=excluded.reason,updated_at=excluded.updated_at`, id, actor.ID, suppressed, reason, stamp(s.now()))
	if e != nil {
		return 0, e
	}
	rows, e := tx.QueryContext(ctx, `SELECT job_id,id,actor_id FROM crash_attempts WHERE instance_id=? AND state IN('pending','queued','admitted')`, id)
	if e != nil {
		return 0, e
	}
	type cancellation struct {
		job, attempt string
		requester    int64
	}
	ids := []cancellation{}
	for rows.Next() {
		var item cancellation
		if e = rows.Scan(&item.job, &item.attempt, &item.requester); e != nil {
			rows.Close()
			return 0, e
		}
		ids = append(ids, item)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return 0, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE crash_attempts SET state='cancelled',reason='Deliberate operator stop.' WHERE instance_id=? AND state IN('pending','queued','admitted')`, id); e != nil {
		return 0, e
	}
	var epoch int64
	if e = tx.QueryRowContext(ctx, "SELECT epoch FROM crash_recovery WHERE instance_id=?", id).Scan(&epoch); e != nil {
		return 0, e
	}
	if e = tx.Commit(); e != nil {
		return 0, e
	}
	for _, item := range ids {
		if cancel, ok := s.recoveryCancels.Load(item.attempt); ok {
			cancel.(context.CancelFunc)()
		}
		if s.config.Jobs != nil && item.job != "" {
			cancelActor := actor
			if owner, err := s.actor(ctx, item.requester); err == nil && owner.Role == domain.RoleAdmin {
				cancelActor = owner
			}
			if err := s.config.Jobs.Cancel(ctx, item.job, cancelActor); err != nil && domain.AsError(err).Code != domain.CodeForbidden {
				return 0, err
			}
		}
	}
	return epoch, nil
}

func (s *Service) retryDelay(ctx context.Context, id string, now time.Time) (time.Duration, bool, error) {
	var count int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM crash_attempts WHERE instance_id=? AND admitted_at!='' AND admitted_at>=?`, id, stamp(now.Add(-10*time.Minute))).Scan(&count)
	if e != nil {
		return 0, false, e
	}
	if count >= 3 {
		return 0, false, nil
	}
	return []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second}[count], true, nil
}

// ObserveRecovery trusts only exact final native exit evidence. A running JVM
// (including an unready JVM), manager restart, missing proof or deliberate exit
// produces no crash retry. Crash notification intent is independent of opt-in.
func (s *Service) ObserveRecovery(ctx context.Context) error {
	if s.config.Native == nil {
		return nil
	}
	// Jobs interrupted before native admission are never blindly replayed.
	if _, e := s.db.ExecContext(ctx, `UPDATE crash_attempts SET state='blocked',reason='Manager interrupted recovery before native admission; review and re-enable recovery.' WHERE state='queued' AND EXISTS(SELECT 1 FROM jobs WHERE id=crash_attempts.job_id AND state IN('failed','cancelled','succeeded'))`); e != nil {
		return e
	}
	rows, e := s.db.QueryContext(ctx, `SELECT i.id,COALESCE(c.enabled,0),COALESCE(c.actor_id,0),COALESCE(c.epoch,0),COALESCE(c.last_generation,'') FROM instances i LEFT JOIN crash_recovery c ON c.instance_id=i.id ORDER BY i.id`)
	if e != nil {
		return e
	}
	type item struct {
		id, last     string
		enabled      bool
		actor, epoch int64
	}
	items := []item{}
	for rows.Next() {
		var it item
		if e = rows.Scan(&it.id, &it.enabled, &it.actor, &it.epoch, &it.last); e != nil {
			rows.Close()
			return e
		}
		items = append(items, it)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	for _, it := range items {
		identity, e := s.config.Native.Identity(ctx, it.id)
		if e != nil {
			continue
		}
		observed, e := s.config.Native.Inspect(ctx, identity)
		if e != nil || !proof(identity, observed, s.now()) {
			continue
		}
		gen := generation(identity)
		if _, e = s.db.ExecContext(ctx, `INSERT INTO scheduler_crash_events(id,instance_id,occurred_at) VALUES(?,?,?) ON CONFLICT DO NOTHING`, "crash:"+gen, it.id, stamp(observed.Exit.ExitedAt)); e != nil {
			return e
		}
		if !it.enabled || gen == it.last {
			continue
		}
		lock := s.recoveryLock(it.id)
		lock.Lock()
		e = s.reserveAttempt(ctx, it.id, gen, it.actor, it.epoch, identity)
		lock.Unlock()
		if e != nil {
			return e
		}
	}
	return s.publishCrashes(ctx)
}
func (s *Service) reserveAttempt(ctx context.Context, id, gen string, actor, epoch int64, identity domain.ProcessIdentity) error {
	if _, e := s.actor(ctx, actor); e != nil {
		_, err := s.db.ExecContext(ctx, `UPDATE crash_recovery SET enabled=0,reason='Recovery owner no longer has authority.' WHERE instance_id=?`, id)
		return err
	}
	delay, allowed, e := s.retryDelay(ctx, id, s.now())
	if e != nil {
		return e
	}
	if !allowed {
		_, e = s.db.ExecContext(ctx, `UPDATE crash_recovery SET reason='Three restart attempts within ten minutes; automatic recovery is suspended.' WHERE instance_id=?`, id)
		return e
	}
	raw, _ := json.Marshal(identity)
	attempt, e := key()
	if e != nil {
		return e
	}
	result, e := s.db.ExecContext(ctx, `INSERT INTO crash_attempts(id,instance_id,epoch,actor_id,generation,identity_json,due_at,state) SELECT ?,?,?,?,?,?,?,'pending' WHERE EXISTS(SELECT 1 FROM crash_recovery WHERE instance_id=? AND enabled=1 AND epoch=? AND last_generation!=?) AND NOT EXISTS(SELECT 1 FROM crash_attempts a LEFT JOIN jobs j ON j.id=a.job_id WHERE a.instance_id=? AND (a.state IN('pending','queued') OR (a.state='blocked' AND a.generation=? AND a.epoch=?) OR (a.state='admitted' AND COALESCE(j.state,'running') IN('queued','running'))))`, attempt, id, epoch, actor, gen, string(raw), stamp(s.now().Add(delay)), id, epoch, gen, id, gen, epoch)
	if e != nil {
		return e
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		_, e = s.db.ExecContext(ctx, `UPDATE crash_recovery SET reason='Retry pending after a proven unexpected exit.' WHERE instance_id=? AND epoch=?`, id, epoch)
	}
	return e
}
func (s *Service) publishCrashes(ctx context.Context) error {
	if s.config.OnCrash == nil {
		return nil
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,instance_id,occurred_at FROM scheduler_crash_events WHERE published=0 ORDER BY occurred_at,id LIMIT 100`)
	if e != nil {
		return e
	}
	events := []CrashEvent{}
	for rows.Next() {
		var event CrashEvent
		var ts string
		if e = rows.Scan(&event.ID, &event.InstanceID, &ts); e != nil {
			rows.Close()
			return e
		}
		event.OccurredAt, e = time.Parse(time.RFC3339Nano, ts)
		if e != nil {
			rows.Close()
			return e
		}
		events = append(events, event)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	for _, event := range events {
		if e = s.config.OnCrash(ctx, event); e != nil {
			return e
		}
		if _, e = s.db.ExecContext(ctx, `UPDATE scheduler_crash_events SET published=1 WHERE id=?`, event.ID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) RegisterJobs() error {
	if s.config.Jobs == nil {
		return domain.E(domain.CodeUnavailable, "recovery job queue unavailable")
	}
	return s.config.Jobs.Register("crash_recovery", s.runRecovery)
}
func (s *Service) dispatchRecovery(ctx context.Context) error {
	if s.config.Jobs == nil || s.config.Lifecycle == nil {
		return nil
	}
	rows, e := s.db.QueryContext(ctx, `SELECT id,instance_id,actor_id FROM crash_attempts WHERE state='pending' AND due_at<=? ORDER BY due_at LIMIT 100`, stamp(s.now()))
	if e != nil {
		return e
	}
	type due struct {
		id, instance string
		actor        int64
	}
	list := []due{}
	for rows.Next() {
		var a due
		if e = rows.Scan(&a.id, &a.instance, &a.actor); e != nil {
			rows.Close()
			return e
		}
		list = append(list, a)
	}
	if e = errors.Join(rows.Err(), rows.Close()); e != nil {
		return e
	}
	for _, a := range list {
		raw, _ := json.Marshal(struct {
			AttemptID string `json:"attempt_id"`
		}{a.id})
		job, e := s.config.Jobs.EnqueueIdempotent(ctx, "recovery:"+a.id, jobs.Request{InstanceID: a.instance, Kind: "crash_recovery", RequestedBy: a.actor, Payload: raw, Cancellable: true})
		if e != nil {
			return e
		}
		if _, e = s.db.ExecContext(ctx, `UPDATE crash_attempts SET state=CASE WHEN state='pending' THEN 'queued' ELSE state END,job_id=? WHERE id=?`, job.ID, a.id); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) runRecovery(ctx context.Context, e *jobs.Execution) (result error) {
	// Bind the original execution before reading any public payload as authority.
	var id, attempt string
	var actor domain.User
	err := e.WithInstanceMutation(ctx, func(instance string) error {
		id = instance
		var durable string
		var requester int64
		if err := s.db.QueryRowContext(ctx, `SELECT payload,requested_by FROM jobs WHERE instance_id=? AND state='running' AND kind='crash_recovery'`, id).Scan(&durable, &requester); err != nil {
			return domain.E(domain.CodeForbidden, "recovery execution unavailable")
		}
		var input struct {
			AttemptID string `json:"attempt_id"`
		}
		if json.Unmarshal([]byte(durable), &input) != nil || !scheduleKey.MatchString(input.AttemptID) {
			return domain.E(domain.CodeForbidden, "invalid recovery intent")
		}
		attempt = input.AttemptID
		var err error
		actor, err = s.actor(ctx, requester)
		return err
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	s.recoveryCancels.Store(attempt, context.CancelFunc(cancel))
	defer func() { s.recoveryCancels.Delete(attempt); cancel() }()
	defer func() {
		cleanup, done := e.CleanupContext()
		defer done()
		if result != nil {
			safe := domain.AsError(result)
			message := safe.Message
			if safe.Code == domain.CodeInternal {
				message = "Recovery failed; inspect native state and recovery history before retrying."
			}
			_ = e.Log(cleanup, message)
			_, _ = s.db.ExecContext(cleanup, `UPDATE crash_attempts SET state='blocked',reason='Recovery failed before native admission; review required.' WHERE id=? AND state IN('pending','queued')`, attempt)
			_, _ = s.db.ExecContext(cleanup, `UPDATE crash_attempts SET reason='Native restart was attempted; inspect the job and current process readiness.' WHERE id=? AND state='admitted'`, attempt)
		} else {
			_, _ = s.db.ExecContext(cleanup, `UPDATE crash_attempts SET reason='Native restart and fresh readiness verification completed.' WHERE id=? AND state='admitted'`, attempt)
		}
	}()
	guard := lifecycle.RecoveryStartGuard(func(ctx context.Context, instance string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
		return s.admitRecoveryStart(ctx, instance, attempt, start)
	})
	return s.config.Lifecycle.StartForRecovery(ctx, e, actor, guard)
}
func (s *Service) admitRecoveryStart(ctx context.Context, id, attempt string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
	var empty domain.ProcessIdentity
	lock := s.recoveryLock(id)
	lock.Lock()
	defer lock.Unlock()
	var raw, gen, state, due string
	var epoch, actor int64
	err := s.db.QueryRowContext(ctx, `SELECT a.identity_json,a.generation,a.state,a.epoch,a.actor_id,a.due_at FROM crash_attempts a JOIN crash_recovery c ON c.instance_id=a.instance_id WHERE a.id=? AND a.instance_id=? AND c.enabled=1 AND c.epoch=a.epoch AND c.last_generation!=a.generation`, attempt, id).Scan(&raw, &gen, &state, &epoch, &actor, &due)
	if err != nil || state != "queued" && state != "pending" {
		return empty, domain.E(domain.CodeConflict, "recovery was cancelled or superseded")
	}
	dueAt, parseErr := time.Parse(time.RFC3339Nano, due)
	if parseErr != nil || s.now().Before(dueAt) {
		return empty, domain.E(domain.CodeConflict, "recovery backoff has not elapsed")
	}
	if _, err = s.actor(ctx, actor); err != nil {
		return empty, err
	}
	var expected domain.ProcessIdentity
	if json.Unmarshal([]byte(raw), &expected) != nil {
		return empty, domain.E(domain.CodeConflict, "invalid recovery generation")
	}
	current, err := s.config.Native.Identity(ctx, id)
	if err != nil || current != expected {
		return empty, domain.E(domain.CodeConflict, "native generation changed before recovery")
	}
	observed, err := s.config.Native.Inspect(ctx, current)
	if err != nil || !proof(current, observed, s.now()) || generation(current) != gen {
		return empty, domain.E(domain.CodeConflict, "exact unexpected exit is no longer proven")
	}
	_, allowed, err := s.retryDelay(ctx, id, s.now())
	if err != nil {
		return empty, err
	}
	if !allowed {
		return empty, domain.E(domain.CodeConflict, "restart retry limit reached")
	}
	// This durable reservation precedes native I/O. A manager crash here consumes
	// an attempt conservatively; the interrupted job is never blindly replayed.
	result, err := s.db.ExecContext(ctx, `UPDATE crash_attempts SET state='admitted',admitted_at=? WHERE id=? AND state IN('pending','queued')`, stamp(s.now()), attempt)
	if err != nil {
		return empty, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return empty, domain.E(domain.CodeConflict, "recovery attempt already consumed")
	}
	return start()
}
