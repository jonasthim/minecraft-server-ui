package jobs

import (
	"context"
	"fmt"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// WithIdle reserves an instance for a stopped-state filesystem/DB mutation.
// Existing queued/running work and overlapping mutations return instance_busy.
// The callback runs without the global queue mutex or a held SQL connection;
// unrelated jobs can continue admission, progress, cancellation, and completion.
//
// Lock order: jobs admission reservation -> native supervisor.WithStopped ->
// filesystem/DB mutation. The callback must never call jobs methods. It captures
// ctx for its own I/O and must return only after mutation/cleanup is finished.
// Cancellation does not release the reservation early. Stop drains callbacks
// together with workers and reports an honest timeout if cleanup has not returned.
func (s *Service) WithIdle(ctx context.Context, instanceID string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateInstanceID(instanceID); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid instance identifier", err)
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "instance mutation callback is required")
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.started || s.stopping || s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeUnavailable, "job service is not accepting instance changes")
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
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE instance_id=? AND state IN ('queued','running'))`, instanceID).Scan(&busy)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("check idle instance jobs: %w", err)
	}
	if busy {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance has queued or running jobs")
	}
	// Add is protected by the same stopping/admission mutex used by dispatch.
	// The loop closes admission under that mutex before waiting for this group.
	s.idle[instanceID] = struct{}{}
	s.workers.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.idle, instanceID)
		s.signal()
		s.mu.Unlock()
		s.workers.Done()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
