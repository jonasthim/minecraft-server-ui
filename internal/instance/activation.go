package instance

import (
	"context"
	"database/sql"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// ConfigActivation is prepared before the config SQL transaction while stopped
// admission is held. Apply may use only the supplied transaction, never query the
// service DB separately. Finish runs after transaction closure and verified file
// recovery under the same stopped boundary and an independent bounded context.
type ConfigActivation interface {
	Apply(context.Context, *sql.Tx) error
	Finish(context.Context) error
}
type ActivationCoordinator interface {
	PrepareConfigActivation(context.Context, string, int) (ConfigActivation, error)
}

func WithActivationCoordinator(coordinator ActivationCoordinator) Option {
	return func(s *Service) { s.activation = coordinator }
}
func (s *Service) prepareConfigActivation(ctx context.Context, previous Detail, memoryMiB int) (ConfigActivation, error) {
	if previous.Provisioning != "installed" || previous.Instance.MemoryMiB == memoryMiB {
		return nil, nil
	}
	if s.activation == nil {
		return nil, domain.E(domain.CodeUnavailable, "installed memory edit requires configuration activation coordination")
	}
	return s.activation.PrepareConfigActivation(ctx, previous.Instance.ID, memoryMiB)
}

// RecoveryMutationGuard is startup-only admission. Its implementation requires
// paused dispatch and reserves this instance while retaining queued work.
type RecoveryMutationGuard interface {
	WithStoppedForRecovery(context.Context, string, func() error) error
}

func WithRecoveryMutationGuard(guard RecoveryMutationGuard) Option {
	return func(s *Service) { s.recoveryGuard = guard }
}
func (s *Service) reconcileMutation(ctx context.Context, id string, fn func() error) error {
	if s.recoveryGuard == nil {
		return s.mutate(ctx, id, fn)
	}
	if err := validID(id); err != nil {
		return err
	}
	return s.recoveryGuard.WithStoppedForRecovery(ctx, id, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	})
}
