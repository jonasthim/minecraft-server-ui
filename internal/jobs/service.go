// Package jobs implements a durable queue with four global execution slots and
// at most one worker per instance. A slot is held until the handler returns,
// including after cancellation while the handler performs cleanup.
package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Job struct {
	domain.Job
	RequestedBy     int64 `json:"requested_by"`
	Progress        int   `json:"progress"`
	Cancellable     bool  `json:"cancellable"`
	CancelRequested bool  `json:"cancel_requested"`
}

// Request payloads are durable private inputs. Do not include credentials: use
// secret-store references. Requester is captured by the authorizing feature API.
type Request struct {
	InstanceID, Kind string
	Payload          json.RawMessage
	RequestedBy      int64
	Cancellable      bool
}
type Handler func(context.Context, *Execution) error
type active struct {
	instance  string
	cancel    context.CancelFunc
	execution *executionIdentity
}
type Service struct {
	db                *sql.DB
	log               *slog.Logger
	hub               *events.Hub
	mu                sync.Mutex
	handlers          map[string]Handler
	running           map[string]active
	idle              map[string]struct{}
	started, stopping bool
	paused            bool
	ctx               context.Context
	cancel            context.CancelFunc
	wake              chan struct{}
	done              chan struct{}
	workers           sync.WaitGroup
}

func New(database *sql.DB, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{db: database, log: log, hub: events.NewHub(32), handlers: make(map[string]Handler), running: make(map[string]active), idle: make(map[string]struct{}), wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// Register freezes a worker kind before startup. Unknown queued kinds fail
// explicitly during recovery, rather than being silently executed as a no-op.
func (s *Service) Register(kind string, handler Handler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopping {
		return domain.E(domain.CodeConflict, "job handlers are frozen")
	}
	if !identifier.MatchString(kind) || handler == nil {
		return domain.E(domain.CodeValidationFailed, "invalid job handler")
	}
	if _, ok := s.handlers[kind]; ok {
		return domain.E(domain.CodeConflict, "job handler already registered")
	}
	s.handlers[kind] = handler
	return nil
}

// Start must run once on the manager's single process. Recoverable queued work
// retains its exact private input. Running work is never automatically replayed;
// future resumable workers must first supply explicit file reconciliation hooks.
func (s *Service) Start(ctx context.Context) error {
	if err := s.StartPaused(ctx); err != nil {
		return err
	}
	return s.Resume()
}

// StartPaused performs durable recovery and opens admission without executing
// queued handlers. Register all kinds first, reconcile stopped/native instance
// state while paused, and call Resume only after composition is ready.
func (s *Service) StartPaused(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.stopping {
		return domain.E(domain.CodeConflict, "job service already started or stopped")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET state='failed',error='Manager interrupted this operation; inspect its last phase before retrying.',finished_at=? WHERE state='running'`, stamp())
	if err != nil {
		return fmt.Errorf("recover interrupted jobs: %w", err)
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.started = true
	s.paused = true
	go s.loop()
	return nil
}

// Resume enables dispatch after StartPaused. Repeated Resume calls are
// idempotent while the service is live. Before startup, after parent-context
// cancellation, or once stopping begins it returns unavailable and never
// reopens admission or dispatch.
func (s *Service) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopping || s.ctx.Err() != nil {
		return domain.E(domain.CodeUnavailable, "job service cannot resume")
	}
	s.paused = false
	s.signal()
	return nil
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) loop() {
	defer func() { s.workers.Wait(); close(s.done) }()
	s.dispatch()
	for {
		select {
		case <-s.ctx.Done():
			s.mu.Lock()
			s.stopping = true
			for _, a := range s.running {
				a.cancel()
			}
			s.mu.Unlock()
			s.hub.Close()
			return
		case <-s.wake:
			s.dispatch()
		}
	}
}

// Stop rejects new jobs immediately, cancels running handlers, closes all live
// subscriptions, and waits for handlers to finish. A deadline reports an honest
// timeout; it never frees an instance or execution slot while a worker runs.
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	s.hub.Close()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	s.cancel()
	s.mu.Unlock()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for job cleanup: %w", ctx.Err())
	}
}
func (s *Service) Enqueue(ctx context.Context, r Request) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.stopping || s.ctx.Err() != nil {
		return Job{}, domain.E(domain.CodeUnavailable, "job service is not accepting work")
	}
	if r.RequestedBy < 0 || !identifier.MatchString(r.Kind) || (r.InstanceID != "" && !identifier.MatchString(r.InstanceID)) {
		return Job{}, domain.E(domain.CodeValidationFailed, "invalid job request")
	}
	if _, reserved := s.idle[r.InstanceID]; reserved {
		return Job{}, domain.E(domain.CodeInstanceBusy, "instance is being changed")
	}
	if _, ok := s.handlers[r.Kind]; !ok {
		return Job{}, domain.E(domain.CodeUnsupported, "job kind is not registered")
	}
	if len(r.Payload) == 0 {
		r.Payload = json.RawMessage(`{}`)
	}
	if len(r.Payload) > 1<<20 || !json.Valid(r.Payload) {
		return Job{}, domain.E(domain.CodeValidationFailed, "invalid job payload")
	}
	if r.InstanceID != "" {
		var exists int
		err := s.db.QueryRowContext(ctx, `SELECT 1 FROM instances WHERE id=?`, r.InstanceID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, domain.NotFound("instance")
		}
		if err != nil {
			return Job{}, fmt.Errorf("check job instance: %w", err)
		}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return Job{}, fmt.Errorf("job id: %w", err)
	}
	id := hex.EncodeToString(raw[:])
	var instance any
	if r.InstanceID != "" {
		instance = r.InstanceID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable) VALUES(?,?,?,'queued',?,?,?,?)`, id, instance, r.Kind, stamp(), string(r.Payload), r.RequestedBy, r.Cancellable)
	if err != nil {
		return Job{}, fmt.Errorf("enqueue job: %w", err)
	}
	j, err := s.Get(ctx, id)
	if err != nil {
		return j, err
	}
	s.publish(j)
	s.signal()
	return j, nil
}
func (s *Service) dispatch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused || s.stopping || s.ctx.Err() != nil {
		return
	}
	pending, err := s.list(s.ctx, `WHERE state='queued' ORDER BY created_at,rowid`)
	if err != nil {
		s.log.Error("job queue unavailable")
		return
	}
	for _, j := range pending {
		if len(s.running) >= 4 {
			return
		}
		if _, reserved := s.idle[j.InstanceID]; reserved {
			continue
		}
		busy := false
		for _, a := range s.running {
			if a.instance == j.InstanceID {
				busy = true
				break
			}
		}
		if busy {
			continue
		}
		handler, ok := s.handlers[j.Kind]
		if !ok {
			_, err := s.db.ExecContext(s.ctx, `UPDATE jobs SET state='failed',error='Worker unavailable; operation was not started.',finished_at=? WHERE id=? AND state='queued'`, stamp(), j.ID)
			if err != nil {
				s.log.Error("fail unavailable job worker")
			}
			continue
		}
		res, err := s.db.ExecContext(s.ctx, `UPDATE jobs SET state='running',started_at=? WHERE id=? AND state='queued'`, stamp(), j.ID)
		if err != nil {
			s.log.Error("claim queued job")
			continue
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.running[j.ID] = active{instance: j.InstanceID, cancel: cancel}
		s.workers.Add(1)
		go s.run(ctx, j.ID, handler)
	}
}
func (s *Service) run(ctx context.Context, id string, handler Handler) {
	defer s.workers.Done()
	j, err := s.Get(ctx, id)
	if err == nil {
		s.publish(j)
		var payload []byte
		payload, err = s.payload(ctx, id)
		if err == nil {
			e := s.bindExecution(ctx, id, j.Job, payload)
			err = invoke(ctx, handler, e)
		}
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := domain.JobSucceeded
	message := ""
	if ctx.Err() != nil {
		state = domain.JobCancelled
		message = "Cancellation completed; inspect the last phase for partial changes."
	} else if err != nil {
		state = domain.JobFailed
		message = "Operation failed; inspect its last phase and safe job output."
	}
	progress := j.Progress
	if state == domain.JobSucceeded {
		progress = 100
	} else {
		if latest, e := s.Get(cleanup, id); e == nil {
			progress = latest.Progress
		}
	}
	_, writeErr := s.db.ExecContext(cleanup, `UPDATE jobs SET state=?,error=?,progress=?,finished_at=? WHERE id=? AND state='running'`, state, message, progress, stamp(), id)
	if writeErr != nil {
		s.log.Error("persist job completion failed; stopping queue", "job_id", id)
		s.stopping = true
		s.cancel()
	} else if latest, e := s.Get(cleanup, id); e == nil {
		s.publish(latest)
	}
	a := s.running[id]
	a.cancel()
	delete(s.running, id)
	s.signal()
}
func invoke(ctx context.Context, h Handler, e *Execution) (err error) {
	defer func() {
		panicValue := recover()
		// Close admission before publishing terminal state, and retain the slot
		// until any callback admitted before handler return has actually finished.
		drainErr := e.finishMutationAdmission()
		if panicValue != nil {
			err = errors.New("job worker panicked")
		}
		if drainErr != nil {
			err = errors.Join(err, drainErr)
		}
	}()
	return h(ctx, e)
}
func (s *Service) publish(j Job) {
	raw, err := json.Marshal(j)
	if err == nil {
		s.hub.Publish(events.Event{Type: "progress", JobID: j.ID, Job: raw})
	}
}

// Subscribe is called only after authorization and loading the selected job.
func (s *Service) Subscribe(jobID string) *events.Subscription { return s.hub.Subscribe(jobID) }

// Cancel is idempotent. An operator can cancel only their own safe job; admins
// may cancel any safe job. System jobs (requester 0) require an administrator.
func (s *Service) Cancel(ctx context.Context, id string, user domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if user.Disabled || !user.Role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "operator access required")
	}
	j, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if user.Role != domain.RoleAdmin && j.RequestedBy != user.ID {
		return domain.E(domain.CodeForbidden, "only the requester or an administrator can cancel this job")
	}
	if j.State == domain.JobSucceeded || j.State == domain.JobFailed || j.State == domain.JobCancelled {
		return nil
	}
	if !j.Cancellable {
		return domain.E(domain.CodeConflict, "this job cannot be safely cancelled")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job cancellation: %w", err)
	}
	defer tx.Rollback()
	if j.State == domain.JobQueued {
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET state='cancelled',cancel_requested=1,finished_at=? WHERE id=? AND state='queued'`, stamp(), id)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE jobs SET cancel_requested=1 WHERE id=? AND state='running'`, id)
	}
	if err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target) VALUES(?,?,?,'job.cancel',?,?)`, stamp(), user.ID, user.Username, j.InstanceID, id)
	if err != nil {
		return fmt.Errorf("audit job cancellation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job cancellation: %w", err)
	}
	if a, ok := s.running[id]; ok {
		a.cancel()
	}
	if latest, e := s.Get(ctx, id); e == nil {
		s.publish(latest)
	}
	return nil
}
