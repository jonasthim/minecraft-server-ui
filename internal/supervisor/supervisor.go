// Package supervisor manages native per-instance launchers. Scheduler policy,
// command acknowledgement and game readiness belong to their consuming packages.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

type Config struct {
	DataDir, Executable string
	StopTimeout         time.Duration
}

// StopFailureCode is a finite, safe classification of a failed Stop.
// StopFailureUnknown means the error did not originate from a classified stop
// path. Callers must not log or expose the error text as a substitute.
type StopFailureCode string

const (
	StopFailureUnknown                   StopFailureCode = "unknown"
	StopFailureLockWait                  StopFailureCode = "lock_wait_failed"
	StopFailureInitialProbe              StopFailureCode = "initial_probe_failed"
	StopFailureProcessProbe              StopFailureCode = "process_probe_failed"
	StopFailureProcessWaitTimeout        StopFailureCode = "process_wait_timeout"
	StopFailureAdmissionWaitTimeout      StopFailureCode = "admission_wait_timeout"
	StopFailureContextCanceled           StopFailureCode = "context_canceled"
	StopFailureInspection                StopFailureCode = "inspection_failed"
	StopFailureMissingFinalMetadata      StopFailureCode = "missing_final_metadata"
	StopFailureForcedTermination         StopFailureCode = "forced_termination"
	StopFailureWatchdogFailure           StopFailureCode = "watchdog_failure"
	StopFailureUncleanExit               StopFailureCode = "unclean_exit"
	StopFailureControlResponseLost       StopFailureCode = "control_response_lost_unclassified"
	StopFailureControlLostProbe          StopFailureCode = "control_response_lost_process_probe_failed"
	StopFailureControlLostProcessTimeout StopFailureCode = "control_response_lost_process_wait_timeout"
	StopFailureControlLostAdmission      StopFailureCode = "control_response_lost_admission_wait_timeout"
	StopFailureControlLostInspection     StopFailureCode = "control_response_lost_inspection_failed"
	StopFailureControlLostMetadata       StopFailureCode = "control_response_lost_missing_final_metadata"
	StopFailureControlLostForced         StopFailureCode = "control_response_lost_forced_termination"
	StopFailureControlLostWatchdog       StopFailureCode = "control_response_lost_watchdog_failure"
	StopFailureControlLostUnclean        StopFailureCode = "control_response_lost_unclean_exit"
)

type stopFailure struct {
	category    StopFailureCode
	err         error
	nativeCause error
}

func (e *stopFailure) Error() string { return e.err.Error() }
func (e *stopFailure) Unwrap() error { return e.err }

// StopFailureCategory returns only a fixed allowlisted category. Raw error
// text is deliberately unavailable through this API.
func StopFailureCategory(err error) StopFailureCode {
	var failure *stopFailure
	if !errors.As(err, &failure) {
		return StopFailureUnknown
	}
	if failure == nil {
		return StopFailureUnknown
	}
	switch failure.category {
	case StopFailureLockWait, StopFailureInitialProbe, StopFailureProcessProbe,
		StopFailureProcessWaitTimeout, StopFailureAdmissionWaitTimeout,
		StopFailureContextCanceled, StopFailureInspection, StopFailureMissingFinalMetadata,
		StopFailureForcedTermination, StopFailureWatchdogFailure, StopFailureUncleanExit,
		StopFailureControlResponseLost, StopFailureControlLostProbe,
		StopFailureControlLostProcessTimeout, StopFailureControlLostAdmission,
		StopFailureControlLostInspection, StopFailureControlLostMetadata,
		StopFailureControlLostForced, StopFailureControlLostWatchdog, StopFailureControlLostUnclean:
		return failure.category
	default:
		return StopFailureUnknown
	}
}

func wrapStopFailure(category StopFailureCode, err error) error {
	if err == nil {
		return nil
	}
	return &stopFailure{category: category, err: err}
}

// StopNativeErrorCode returns a bounded native error number only for a known
// Stop failure. Error text is never a diagnostic source.
func StopNativeErrorCode(err error) (uint32, bool) {
	if StopFailureCategory(err) == StopFailureUnknown {
		return 0, false
	}
	var failure *stopFailure
	if !errors.As(err, &failure) || failure == nil {
		return 0, false
	}
	cause := failure.err
	if failure.nativeCause != nil {
		cause = failure.nativeCause
	}
	var code syscall.Errno
	if !errors.As(cause, &code) || code == 0 || uint64(code) > 65535 {
		return 0, false
	}
	return uint32(code), true
}

type Observation struct {
	State          domain.InstanceState
	ProcessRunning bool
	Exit           *launcher.Exit
	WatchdogError  string
}
type child struct {
	stdin *os.File
	done  chan struct{}
	err   error
}
type Supervisor struct {
	cfg         Config
	mu          sync.Mutex
	locks       map[string]chan struct{}
	children    map[string]*child
	starts      sync.WaitGroup
	closing     bool
	sendControl func(context.Context, domain.ProcessIdentity, string, launcher.Request) (launcher.Response, error)
}

var _ domain.ProcessSupervisor = (*Supervisor)(nil)

func New(cfg Config) (*Supervisor, error) {
	if !filepath.IsAbs(cfg.DataDir) {
		return nil, fmt.Errorf("data directory must be absolute")
	}
	cfg.DataDir = filepath.Clean(cfg.DataDir)
	if cfg.Executable == "" {
		var err error
		cfg.Executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	if !filepath.IsAbs(cfg.Executable) {
		return nil, fmt.Errorf("launcher executable must be absolute")
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 90 * time.Second
	}
	if cfg.StopTimeout > 2*time.Minute {
		return nil, fmt.Errorf("stop timeout exceeds two minutes")
	}
	return &Supervisor{
		cfg: cfg, locks: map[string]chan struct{}{}, children: map[string]*child{},
		sendControl: func(ctx context.Context, id domain.ProcessIdentity, path string, req launcher.Request) (launcher.Response, error) {
			return launcher.Send(ctx, cfg.DataDir, id, path, req)
		},
	}, nil
}
func (s *Supervisor) lock(ctx context.Context, id string) (func(), error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return nil, domain.Wrap(domain.CodeValidationFailed, "invalid instance id", err)
	}
	if err := ctx.Err(); err != nil {
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
func (s *Supervisor) Start(ctx context.Context, id string, spec domain.LaunchSpec) (domain.ProcessIdentity, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return domain.ProcessIdentity{}, domain.E(domain.CodeUnavailable, "supervisor is shutting down")
	}
	s.starts.Add(1)
	s.mu.Unlock()
	defer s.starts.Done()
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return domain.ProcessIdentity{}, err
	}
	p, err := launcher.Paths(s.cfg.DataDir, id)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	if err = launcher.ValidateSpec(spec, p); err != nil {
		return domain.ProcessIdentity{}, domain.Wrap(domain.CodeValidationFailed, "invalid launch specification", err)
	}
	if err = s.requireStopped(ctx, id); err != nil {
		return domain.ProcessIdentity{}, err
	}
	if err = launcher.CheckActivationReady(ctx, s.cfg.DataDir, id); err != nil {
		return domain.ProcessIdentity{}, err
	}
	admission, err := launcher.AcquireAdmission(s.cfg.DataDir, id)
	if err != nil {
		return domain.ProcessIdentity{}, domain.Wrap(domain.CodeInstanceRunning, "launcher active", err)
	}
	// Check again after acquiring the cross-process lock to close a concurrent
	// launch window. The external launcher takes this same lock before spawn.
	old, readErr := s.identity(id)
	if readErr == nil {
		alive, e := s.probe(ctx, old)
		if e != nil || alive {
			admission()
			return domain.ProcessIdentity{}, domain.Wrap(domain.CodeInstanceRunning, "previous process still active or unverifiable", e)
		}
	} else if !os.IsNotExist(readErr) {
		admission()
		return domain.ProcessIdentity{}, readErr
	}
	err = launcher.Seal(ctx, s.cfg.DataDir, id, spec)
	admission()
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	if err = s.platformStart(ctx, id); err != nil {
		return domain.ProcessIdentity{}, err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()
	for {
		identity, e := s.identity(id)
		if e == nil && (identity.StartIdentity != old.StartIdentity || identity.PID != old.PID) {
			if alive, e := s.probe(ctx, identity); e == nil && alive {
				if _, e = launcher.Send(ctx, s.cfg.DataDir, identity, "/status", launcher.Request{}); e == nil {
					return identity, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return domain.ProcessIdentity{}, ctx.Err()
		case <-timeout.C:
			return domain.ProcessIdentity{}, domain.E(domain.CodeUnavailable, "launcher did not publish a live JVM identity; inspect service and console logs")
		case <-ticker.C:
		}
	}
}
func (s *Supervisor) identity(id string) (domain.ProcessIdentity, error) {
	p, err := launcher.Paths(s.cfg.DataDir, id)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	var identity domain.ProcessIdentity
	err = launcher.ReadJSON(filepath.Join(p.State, "identity.json"), &identity)
	if err == nil && identity.InstanceID != id {
		return domain.ProcessIdentity{}, fmt.Errorf("persisted identity instance mismatch")
	}
	return identity, err
}

// Identity loads private JVM metadata for restart reconciliation. It does not
// assert liveness; consumers must call Inspect before using process telemetry.
func (s *Supervisor) Identity(ctx context.Context, id string) (domain.ProcessIdentity, error) {
	if err := ctx.Err(); err != nil {
		return domain.ProcessIdentity{}, err
	}
	return s.identity(id)
}
func (s *Supervisor) Inspect(ctx context.Context, id domain.ProcessIdentity) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{State: domain.StateUnknown}, err
	}
	alive, err := s.probe(ctx, id)
	if err != nil {
		return Observation{State: domain.StateUnknown}, err
	}
	if alive {
		response, err := launcher.Send(ctx, s.cfg.DataDir, id, "/status", launcher.Request{})
		if err != nil {
			return Observation{State: domain.StateUnknown, ProcessRunning: true}, err
		}
		state := domain.StateRunning
		if response.Stopping {
			state = domain.StateStopping
		}
		return Observation{State: state, ProcessRunning: true, WatchdogError: response.WatchdogError}, nil
	}
	p, err := launcher.Paths(s.cfg.DataDir, id.InstanceID)
	if err != nil {
		return Observation{State: domain.StateUnknown}, err
	}
	var exit launcher.Exit
	if err = launcher.ReadJSON(filepath.Join(p.State, "exit.json"), &exit); err == nil && exit.Identity == id {
		state := domain.StateCrashed
		if exit.Deliberate {
			state = domain.StateStopped
		}
		return Observation{State: state, Exit: &exit, WatchdogError: exit.WatchdogError}, nil
	}
	// A stop intent survives a launcher/service crash between stop and exit record.
	var intent launcher.Exit
	if err = launcher.ReadJSON(filepath.Join(p.State, "stop.json"), &intent); err == nil && intent.Identity == id && intent.Deliberate {
		return Observation{State: domain.StateStopped, Exit: &intent}, nil
	}
	return Observation{State: domain.StateCrashed}, nil
}
func (s *Supervisor) Status(ctx context.Context, id domain.ProcessIdentity) (domain.InstanceState, error) {
	o, err := s.Inspect(ctx, id)
	return o.State, err
}
func (s *Supervisor) Command(ctx context.Context, id domain.ProcessIdentity, command string) error {
	if err := launcher.ValidateCommand(command); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid console command", err)
	}
	_, err := launcher.Send(ctx, s.cfg.DataDir, id, "/command", launcher.Request{Command: command})
	return err
}
func (s *Supervisor) ArmSaveLease(ctx context.Context, id domain.ProcessIdentity, leaseID string, expires time.Time) error {
	_, err := launcher.Send(ctx, s.cfg.DataDir, id, "/lease/arm", launcher.Request{LeaseID: leaseID, ExpiresAt: expires})
	return err
}
func (s *Supervisor) ReleaseSaveLease(ctx context.Context, id domain.ProcessIdentity, leaseID string) error {
	_, err := launcher.Send(ctx, s.cfg.DataDir, id, "/lease/release", launcher.Request{LeaseID: leaseID})
	return err
}
func (s *Supervisor) Stop(ctx context.Context, id domain.ProcessIdentity) error {
	unlock, err := s.lock(ctx, id.InstanceID)
	if err != nil {
		return wrapStopFailure(StopFailureLockWait, err)
	}
	defer unlock()
	return s.stop(ctx, id)
}
func (s *Supervisor) stop(ctx context.Context, id domain.ProcessIdentity) error {
	alive, err := s.probe(ctx, id)
	if err != nil {
		return wrapStopFailure(StopFailureInitialProbe, err)
	}
	if !alive {
		wait, cancel := context.WithTimeout(ctx, s.cfg.StopTimeout+10*time.Second)
		defer cancel()
		return s.waitExit(wait, id)
	}
	if _, err = s.sendControl(ctx, id, "/stop", launcher.Request{}); err != nil {
		return s.reconcileStopDeliveryFailure(ctx, id, err)
	}
	wait, cancel := context.WithTimeout(ctx, s.cfg.StopTimeout+10*time.Second)
	defer cancel()
	return s.waitExit(wait, id)
}

// A control connection can fail after the launcher has accepted and persisted
// the stop intent. Treat that transport result as successful only when the
// normal identity, admission, and clean-exit checks confirm completion.
func (s *Supervisor) reconcileStopDeliveryFailure(ctx context.Context, id domain.ProcessIdentity, deliveryErr error) error {
	wait, cancel := context.WithTimeout(ctx, s.cfg.StopTimeout+10*time.Second)
	defer cancel()
	if err := s.waitExit(wait, id); err == nil {
		return nil
	} else {
		return stopDeliveryFailure(deliveryErr, err)
	}
}

func stopDeliveryFailure(deliveryErr, completionErr error) error {
	return &stopFailure{
		category: stopControlLostCategory(StopFailureCategory(completionErr)),
		err:      domain.Wrap(domain.CodeUnavailable, "stop delivery failed and clean completion was not confirmed", errors.Join(deliveryErr, completionErr)),
		// Attribute the native code to the completion failure that determined the
		// category, while retaining both original causes for errors.Is/As.
		nativeCause: completionErr,
	}
}

func stopControlLostCategory(inner StopFailureCode) StopFailureCode {
	switch inner {
	case StopFailureProcessProbe:
		return StopFailureControlLostProbe
	case StopFailureProcessWaitTimeout:
		return StopFailureControlLostProcessTimeout
	case StopFailureAdmissionWaitTimeout:
		return StopFailureControlLostAdmission
	case StopFailureInspection:
		return StopFailureControlLostInspection
	case StopFailureMissingFinalMetadata:
		return StopFailureControlLostMetadata
	case StopFailureForcedTermination:
		return StopFailureControlLostForced
	case StopFailureWatchdogFailure:
		return StopFailureControlLostWatchdog
	case StopFailureUncleanExit:
		return StopFailureControlLostUnclean
	default:
		return StopFailureControlResponseLost
	}
}

func stopWaitContextFailure(err error, lastObservedAlive bool) error {
	if errors.Is(err, context.Canceled) {
		return wrapStopFailure(StopFailureContextCanceled, err)
	}
	if lastObservedAlive {
		return wrapStopFailure(StopFailureProcessWaitTimeout, err)
	}
	return wrapStopFailure(StopFailureAdmissionWaitTimeout, err)
}

func (s *Supervisor) waitExit(ctx context.Context, id domain.ProcessIdentity) error {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	lastObservedAlive := true
	for {
		alive, err := s.probe(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return stopWaitContextFailure(ctx.Err(), lastObservedAlive)
			}
			return wrapStopFailure(StopFailureProcessProbe, err)
		}
		lastObservedAlive = alive
		if !alive {
			// Wait for launcher bookkeeping/lock release as well as the JVM itself.
			release, e := launcher.AcquireAdmission(s.cfg.DataDir, id.InstanceID)
			if e == nil {
				release()
				o, e := s.Inspect(ctx, id)
				if e != nil {
					return wrapStopFailure(StopFailureInspection, e)
				}
				if o.Exit != nil && o.Exit.Forced {
					return wrapStopFailure(StopFailureForcedTermination, domain.E(domain.CodeInternal, "JVM required forced termination"))
				}
				if o.Exit == nil || o.Exit.ExitedAt.IsZero() {
					return wrapStopFailure(StopFailureMissingFinalMetadata, domain.E(domain.CodeInternal, "JVM exited without final launcher exit metadata"))
				}
				if o.Exit.WatchdogError != "" {
					return wrapStopFailure(StopFailureWatchdogFailure, domain.E(domain.CodeInternal, "launcher reported a control or save recovery failure"))
				}
				if !o.Exit.Deliberate || o.Exit.ExitCode != 0 {
					return wrapStopFailure(StopFailureUncleanExit, domain.E(domain.CodeInternal, "JVM completion was not a clean deliberate stop"))
				}

				return nil
			}
		}
		select {
		case <-ctx.Done():
			return stopWaitContextFailure(ctx.Err(), lastObservedAlive)
		case <-tick.C:
		}
	}
}
func (s *Supervisor) SetAutostart(ctx context.Context, id string, enabled bool) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	release, err := launcher.AcquireAdmission(s.cfg.DataDir, id)
	if err == nil {
		release()
	} else if enabled {
		// An active launcher already owns the stable lock file; verify its live
		// identity before allowing boot policy changes.
		identity, e := s.identity(id)
		if e != nil {
			return err
		}
		alive, e := s.probe(ctx, identity)
		if e != nil || !alive {
			return err
		}
	}
	return s.platformSetAutostart(ctx, id, enabled)
}

// WithStopped excludes lifecycle operations in this manager and JVM launch in
// every launcher process. fn must never call back into the supervisor or jobs.
func (s *Supervisor) WithStopped(ctx context.Context, id string, fn func() error) error {
	unlock, err := s.lock(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	release, err := launcher.AcquireAdmission(s.cfg.DataDir, id)
	if err != nil {
		return domain.Wrap(domain.CodeInstanceRunning, "instance launcher is active", err)
	}
	defer release()
	if err = s.requireStopped(ctx, id); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return fn()
}
func (s *Supervisor) requireStopped(ctx context.Context, id string) error {
	if err := s.platformRequireStopped(ctx, id); err != nil {
		return err
	}
	identity, err := s.identity(id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	alive, err := s.probe(ctx, identity)
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "cannot verify stopped process", err)
	}
	if alive {
		return domain.E(domain.CodeInstanceRunning, "instance process is running")
	}
	return nil
}
func (s *Supervisor) Autostart(ctx context.Context, specs map[string]domain.LaunchSpec) error {
	return s.platformAutostart(ctx, specs)
}
func (s *Supervisor) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.starts.Wait(); close(done) }()
	select {
	case <-done:
		return s.platformShutdown(ctx)
	case <-ctx.Done():
		return fmt.Errorf("wait for in-flight process starts: %w", ctx.Err())
	}
}
func sortedIDs(specs map[string]domain.LaunchSpec) []string {
	ids := make([]string, 0, len(specs))
	for id := range specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *Supervisor) probe(ctx context.Context, id domain.ProcessIdentity) (bool, error) {
	alive, err := launcher.ProbeIdentityContext(ctx, id)
	if err != nil || !alive {
		return alive, err
	}
	paths, err := launcher.Paths(s.cfg.DataDir, id.InstanceID)
	if err != nil {
		return false, err
	}
	if filepath.Clean(id.WorkingDir) != paths.Server {
		return false, fmt.Errorf("live identity working directory differs from configured instance")
	}
	return true, nil
}
