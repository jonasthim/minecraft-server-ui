package jobs

import (
	"context"
	"fmt"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// WithObservation reserves a live, resumed instance for a read-only callback.
// Admission remains open: Enqueue persists work while dispatch waits for this
// callback to return. Existing queued/running work takes priority over new reads.
//
// The callback must not call queue methods or mutate controlled server state.
// It captures ctx for bounded I/O and runs without the queue mutex or a held SQL
// connection. Cancellation and panic unwinding retain the reservation through
// deferred callback cleanup. Stop refuses new observations and drains callbacks
// together with workers, reporting an honest timeout if cleanup has not returned.
func (s *Service) WithObservation(ctx context.Context, instanceID string, fn func() error) error {
	if ctx == nil {
		return domain.E(domain.CodeValidationFailed, "instance observation context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := domain.ValidateInstanceID(instanceID); err != nil {
		return domain.Wrap(domain.CodeValidationFailed, "invalid instance identifier", err)
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "instance observation callback is required")
	}
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if !s.started || s.paused || s.stopping || s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeUnavailable, "job service is not accepting instance observations")
	}
	_, mutating := s.idle[instanceID]
	_, observing := s.observing[instanceID]
	if mutating || observing {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance is being changed or observed")
	}
	var busy bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE instance_id=? AND state IN ('queued','running'))`, instanceID).Scan(&busy)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("check observed instance jobs: %w", err)
	}
	if busy {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "instance has queued or running jobs")
	}
	// Admission and the loop's transition to worker drain share this mutex.
	s.observing[instanceID] = struct{}{}
	s.workers.Add(1)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.observing, instanceID)
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
