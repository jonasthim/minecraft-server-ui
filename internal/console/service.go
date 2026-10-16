package console

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type Native interface {
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
	Command(context.Context, domain.ProcessIdentity, string) error
	ReleaseSaveLease(context.Context, domain.ProcessIdentity, string) error
}
type Instances interface {
	Get(context.Context, string) (instance.Detail, error)
	Management(context.Context, string) (instance.ManagementConfig, error)
}
type cachedState struct {
	identity domain.ProcessIdentity
	state    State
}
type captureState struct {
	available bool
	observed  time.Time
}
type Service struct {
	db        *sql.DB
	paths     instance.Paths
	native    Native
	instances Instances
	queue     *jobs.Service
	history   *History
	mu        sync.Mutex
	locks     map[string]chan struct{}
	states    map[string]cachedState
	captures  map[string]captureState
	streams   atomic.Int32
}

func New(database *sql.DB, paths instance.Paths, native Native, instances Instances, queue *jobs.Service) *Service {
	return &Service{db: database, paths: paths, native: native, instances: instances, queue: queue, history: NewHistory(database, paths), locks: map[string]chan struct{}{}, states: map[string]cachedState{}, captures: map[string]captureState{}}
}
func (s *Service) Register() error {
	if s.queue == nil {
		return domain.E(domain.CodeUnavailable, "console command queue is unavailable")
	}
	return s.queue.Register("console_command", s.runCommand)
}
func (s *Service) lock(ctx context.Context, id string) (func(), error) {
	if _, err := s.paths.For(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	ch := s.locks[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.locks[id] = ch
	}
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Service) History(ctx context.Context, id string, after int64, limit int) (Page, error) {
	return s.history.Read(ctx, id, after, limit)
}

// Capture is private ingestion. A failure leaves the last committed redacted
// cursor intact. Callers expose only a safe availability message, never raw I/O.
func (s *Service) Capture(ctx context.Context, id string) (captureErr error) {
	defer func() {
		s.mu.Lock()
		s.captures[id] = captureState{available: captureErr == nil, observed: time.Now().UTC()}
		s.mu.Unlock()
	}()
	if s.instances == nil {
		return domain.E(domain.CodeUnavailable, "instance metadata is unavailable")
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return err
	}
	if cfg, err := s.instances.Management(ctx, id); err == nil {
		s.history.SetSecrets(id, []string{cfg.Secret})
	} else if _, modern := instance.ManagementProtocol(detail.Instance.MinecraftVersion); modern == nil {
		return domain.E(domain.CodeUnavailable, "private log redaction configuration is unavailable")
	}
	return s.history.Capture(ctx, id)
}
func (s *Service) Status(ctx context.Context, id string) (state State, resultErr error) {
	defer func() {
		s.mu.Lock()
		capture, ok := s.captures[id]
		s.mu.Unlock()
		state.LogsAvailable = ok && capture.available && time.Since(capture.observed) <= 15*time.Second
		state.LogsObservedAt = capture.observed
		state.LogsReason = "Live log ingestion has not been observed; retained history may be incomplete."
		if ok && !capture.available {
			state.LogsReason = "Live log ingestion is unavailable; retained history may be incomplete."
		} else if state.LogsAvailable {
			state.LogsReason = "Redacted log ingestion is active."
		}
	}()
	state = State{Status: "unknown", Reason: "Fresh native readiness has not been verified.", GameplayEffect: "not_verified", RuleScope: "native_server_rules", ObservedAt: time.Now().UTC()}
	if s.instances == nil || s.native == nil {
		return state, domain.E(domain.CodeUnavailable, "native console observation is unavailable")
	}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return state, err
	}
	identity, err := s.native.Identity(ctx, id)
	if os.IsNotExist(err) {
		state.Status = "stopped"
		state.Reason = "No managed JVM identity has been recorded."
		return state, nil
	}
	if err != nil {
		return state, domain.E(domain.CodeUnavailable, "managed JVM identity is unavailable")
	}
	observation, err := s.native.Inspect(ctx, identity)
	if err != nil {
		return state, domain.E(domain.CodeUnavailable, "managed JVM state could not be verified")
	}
	state.ProcessRunning = observation.ProcessRunning
	if !observation.ProcessRunning {
		state.Status = string(observation.State)
		state.Reason = "The managed JVM is not running."
		return state, nil
	}
	if observation.State != domain.StateRunning || observation.WatchdogError != "" {
		state.Reason = "The managed JVM is stopping or has an unresolved control failure."
		return state, nil
	}
	s.mu.Lock()
	cached, ok := s.states[id]
	s.mu.Unlock()
	if ok && cached.identity == identity && time.Since(cached.state.ObservedAt) <= 15*time.Second {
		return cached.state, nil
	}
	return state, nil
}
func (s *Service) store(id domain.ProcessIdentity, state State) {
	state.ObservedAt = time.Now().UTC()
	s.mu.Lock()
	s.states[id.InstanceID] = cachedState{id, state}
	s.mu.Unlock()
}

// Refresh is an idle, read-only native probe; it never applies desired settings.
// A lifecycle job calls Session.VerifyReady to apply/read back required rules.
func (s *Service) Refresh(ctx context.Context, id string) error {
	if s.queue == nil {
		return domain.E(domain.CodeUnavailable, "console command queue is unavailable")
	}
	err := s.queue.WithIdle(ctx, id, func() error {
		return s.withSession(ctx, id, func(session *Session) error { _, err := session.verify(ctx, false); return err })
	})
	if err != nil && domain.AsError(err).Code != domain.CodeInstanceBusy {
		s.mu.Lock()
		delete(s.states, id)
		s.mu.Unlock()
	}
	return err
}

// Run uses four fixed workers and a bounded queue; instance activity is deduped.
// Capture and probes stop with ctx. Raw launcher logs continue independently.
func (s *Service) Run(ctx context.Context) error {
	if s.db == nil || s.instances == nil || s.native == nil || s.queue == nil {
		return domain.E(domain.CodeUnavailable, "console service is not configured")
	}
	work := make(chan string, 4)
	var workers sync.WaitGroup
	var pendingMu sync.Mutex
	pending := map[string]bool{}
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range work {
				_ = s.Capture(ctx, id)
				_ = s.Refresh(ctx, id)
				pendingMu.Lock()
				delete(pending, id)
				pendingMu.Unlock()
			}
		}()
	}
	defer func() { close(work); workers.Wait() }()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM instances ORDER BY id`)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("list console instances: %w", err)
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
			return err
		}
		for _, id := range ids {
			pendingMu.Lock()
			active := pending[id]
			if !active {
				pending[id] = true
			}
			pendingMu.Unlock()
			if active {
				continue
			}
			select {
			case work <- id:
			case <-ctx.Done():
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// AcquireStream bounds the total number of allocated live console handlers.
func (s *Service) AcquireStream(ctx context.Context, id string) (func(), error) {
	if _, err := s.history.Read(ctx, id, 0, 1); err != nil {
		return nil, err
	}
	if s.streams.Add(1) > 64 {
		s.streams.Add(-1)
		return nil, domain.E(domain.CodeUnavailable, "console stream limit reached; retry shortly")
	}
	var once sync.Once
	return func() { once.Do(func() { s.streams.Add(-1) }) }, nil
}

type commandRequest struct {
	Text    string `json:"command"`
	ActorID int64  `json:"actor_id"`
}

func (s *Service) EnqueueCommand(ctx context.Context, id, text string, actor domain.User) (jobs.Job, error) {
	return s.enqueueCommand(ctx, "", id, text, actor)
}
func (s *Service) enqueueCommand(ctx context.Context, key, id, text string, actor domain.User) (jobs.Job, error) {
	if actor.Disabled || actor.ID <= 0 {
		return jobs.Job{}, domain.E(domain.CodeForbidden, "active operator access required")
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	command, err := ValidateCommand(selectionOf(detail), actor.Role, text)
	if err != nil {
		return jobs.Job{}, err
	}
	if s.queue == nil {
		return jobs.Job{}, domain.E(domain.CodeUnavailable, "console command queue is unavailable")
	}
	// Audit the request without announcement contents or sensitive output. An
	// enqueue failure is still an attempted command, never an execution claim.
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,'console.command.request',?,?,'{}')`, time.Now().UTC().Format(time.RFC3339Nano), actor.ID, actor.Username, id, command.Verb)
	if err != nil {
		return jobs.Job{}, fmt.Errorf("audit console request: %w", err)
	}
	payload, err := json.Marshal(commandRequest{text, actor.ID})
	if err != nil {
		return jobs.Job{}, err
	}
	request := jobs.Request{InstanceID: id, Kind: "console_command", Payload: payload, RequestedBy: actor.ID, Cancellable: true}
	if key != "" {
		return s.queue.EnqueueIdempotent(ctx, key, request)
	}
	return s.queue.Enqueue(ctx, request)
}
func (s *Service) runCommand(ctx context.Context, e *jobs.Execution) error {
	var input commandRequest
	if json.Unmarshal(e.Payload, &input) != nil || input.ActorID <= 0 {
		return domain.E(domain.CodeValidationFailed, "invalid private command job")
	}
	var actor domain.User
	actor.ID = input.ActorID
	if err := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, actor.ID).Scan(&actor.Username, &actor.Role, &actor.Disabled); err != nil {
		return domain.E(domain.CodeForbidden, "command requester is unavailable")
	}
	if actor.Disabled {
		return domain.E(domain.CodeForbidden, "command requester is disabled")
	}
	return s.WithJobSession(ctx, e, func(session *Session) error {
		command, err := ValidateCommand(selectionOf(session.scope.detail), actor.Role, input.Text)
		if err != nil {
			return err
		}
		if err = e.Phase(ctx, "awaiting_acknowledgement", 25); err != nil {
			return err
		}
		result, err := session.public(ctx, command)
		if err != nil {
			return err
		}
		if !result.Verified {
			return domain.E(domain.CodeUnavailable, "command was delivered without a verifiable acknowledgement")
		}
		return e.Log(ctx, "Server acknowledged the approved "+command.Verb+" command: "+result.Output)
	})
}
func selectionOf(d instance.Detail) domain.VersionSelection {
	return domain.VersionSelection{Flavor: d.Instance.Flavor, MinecraftVersion: d.Instance.MinecraftVersion, LoaderVersion: d.Instance.LoaderVersion}
}
