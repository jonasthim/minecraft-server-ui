package instance

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// WithStoppedRecovery provides the same original callback capability as a job
// session, under the configured paused-startup/native admission. It never falls
// back to ordinary idle admission, which would reject retained queued jobs.
func (s *Service) WithStoppedRecovery(ctx context.Context, id string, fn func(*StoppedSession) error) error {
	if s.recoveryGuard == nil {
		return domain.E(domain.CodeUnavailable, "stopped startup recovery admission unavailable")
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "stopped recovery callback required")
	}
	if err := validID(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.recoveryGuard.WithStoppedForRecovery(ctx, id, func() error { return s.withStoppedSession(ctx, id, fn) })
}
