package jobs

import (
	"context"
	"fmt"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// WithPausedInstance reserves an instance for startup reconciliation after
// StartPaused succeeds and before Resume enables dispatch. Retained queued work
// is allowed, while running jobs and overlapping idle/recovery callbacks return
// instance_busy. Resume returns instance_busy while any recovery callback is
// active; callbacks never implicitly resume dispatch.
//
// Compose jobs recovery reservation -> native supervisor.WithStopped ->
// filesystem/DB reconciliation. The callback must never call jobs methods or
// recursively acquire these guards. It runs without the global queue mutex or
// a held SQL connection, captures ctx for I/O, and must return only after cleanup
// is finished. Caller cancellation and panic unwinding retain the reservation
// through cleanup. Stop closes admission, cancels the service, and drains these
// callbacks with workers, reporting its deadline if cleanup remains active.
func (s *Service) WithPausedInstance(ctx context.Context, instanceID string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateInstanceID(instanceID); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid instance identifier", err)
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "instance recovery callback is required")
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.started || !s.paused || s.stopping || s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeUnavailable, "job service is not accepting startup recovery")
	}
	if _, reserved := s.idle[instanceID]; reserved {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance is being changed")
	}
	if _, reserved := s.observing[instanceID]; reserved {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance is being observed")
	}
	var busy bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE instance_id=? AND state='running')`, instanceID).Scan(&busy)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("check startup instance jobs: %w", err)
	}
	if busy {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance has running jobs")
	}
	// Sharing the idle reservation makes admission atomic against Enqueue and
	// WithIdle. The loop closes admission under this mutex before worker drain.
	s.idle[instanceID] = struct{}{}
	s.recovering++
	s.workers.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.idle, instanceID)
		s.recovering--
		s.signal()
		s.mu.Unlock()
		s.workers.Done()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return domain.E(domain.CodeUnavailable, "job service is stopping")
	}
	return fn()
}
