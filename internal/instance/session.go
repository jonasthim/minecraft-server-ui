package instance

import (
	"context"
	"os"
	"sync"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

// JobMutationGuard must compose private execution.WithInstanceMutation followed
// by native.WithStopped exactly once, passing the admission's authoritative id.
// Its callback runs without global queue locks or an idle guard reacquisition.
type JobMutationGuard interface {
	WithStoppedForJob(context.Context, *jobs.Execution, func(string) error) error
}
type Option func(*Service)

func WithJobMutationGuard(guard JobMutationGuard) Option {
	return func(s *Service) { s.jobGuard = guard }
}

type stoppedScope struct {
	owner   *StoppedSession
	active  atomic.Bool
	mu      sync.Mutex
	ctx     context.Context
	service *Service
	id      string
}

// StoppedSession can only be used as the original callback argument, during that
// callback's lifetime. Methods reject overlap and never reacquire queue/native.
type StoppedSession struct{ scope *stoppedScope }

func (s *Service) WithStoppedJob(ctx context.Context, execution *jobs.Execution, fn func(*StoppedSession) error) error {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "stopped session callback is required")
	}
	if s.jobGuard == nil {
		return domain.E(domain.CodeUnavailable, "current-job lifecycle verification is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.jobGuard.WithStoppedForJob(ctx, execution, func(id string) error {
		if err := validID(id); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.recoverDeletion(ctx, id); err != nil {
			return err
		}
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		paths, err := s.paths.For(id)
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(paths.Root)
		if err != nil {
			return err
		}
		err = s.verifyOwner(ctx, id, root)
		root.Close()
		if err != nil {
			return err
		}
		session := &StoppedSession{}
		scope := &stoppedScope{owner: session, ctx: ctx, service: s, id: id}
		session.scope = scope
		scope.active.Store(true)
		defer func() { scope.active.Store(false); scope.mu.Lock(); scope.mu.Unlock() }()
		return fn(session)
	})
}
func (s *StoppedSession) enter(ctx context.Context) (*stoppedScope, func(), error) {
	if s == nil || s.scope == nil || s.scope.owner != s || !s.scope.active.Load() {
		return nil, nil, domain.E(domain.CodeForbidden, "stopped session is invalid or expired")
	}
	scope := s.scope
	if !scope.mu.TryLock() {
		return nil, nil, domain.E(domain.CodeInstanceBusy, "another stopped session method is active")
	}
	if !scope.active.Load() {
		scope.mu.Unlock()
		return nil, nil, domain.E(domain.CodeForbidden, "stopped session is expired")
	}
	if err := scope.ctx.Err(); err != nil {
		scope.mu.Unlock()
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		scope.mu.Unlock()
		return nil, nil, err
	}
	return scope, scope.mu.Unlock, nil
}
func (s *StoppedSession) Detail(ctx context.Context) (Detail, error) {
	scope, release, err := s.enter(ctx)
	if err != nil {
		return Detail{}, err
	}
	defer release()
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	return scope.service.Get(operation, scope.id)
}
func (s *StoppedSession) Paths(ctx context.Context) (Directories, error) {
	scope, release, err := s.enter(ctx)
	if err != nil {
		return Directories{}, err
	}
	defer release()
	return scope.service.paths.For(scope.id)
}
func (s *StoppedSession) Materialize(ctx context.Context) error {
	scope, release, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer release()
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	return scope.service.materialize(operation, scope.id)
}

// RuntimeSettings returns desired native-rule requirements, never evidence of
// applied rules or gameplay effects. It shares the session's lifetime checks.
func (s *StoppedSession) RuntimeSettings(ctx context.Context) ([]DesiredRuntimeSetting, error) {
	detail, err := s.Detail(ctx)
	if err != nil {
		return nil, err
	}
	return detail.Settings.DesiredRuntime, nil
}

func sessionContext(caller, parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() { stop(); cancel() }
}
