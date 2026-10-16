package lifecycle

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

// EnqueueRestartOnce uses the reviewed countdown/player/acknowledgement path.
// This capability cannot request emergency skip, install or arbitrary lifecycle
// operations. The lifecycle worker reauthorizes the original account.
func (s *Service) EnqueueRestartOnce(ctx context.Context, key, id string, countdown int, actor domain.User) (jobs.Job, error) {
	if key == "" {
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "scheduled restart admission requires a durable key")
	}
	if err := s.actor(ctx, actor.ID, false); err != nil {
		return jobs.Job{}, err
	}
	return s.enqueue(ctx, key, actor, id, Request{Operation: "restart", CountdownSeconds: &countdown})
}

// RecoveryStartGuard fences only native start; readiness and preparation never
// retain the scheduler stop lock. The callback must be invoked at most once.
type RecoveryStartGuard func(context.Context, string, func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error)

// StartForRecovery admits only the original running crash-recovery execution
// and its durable requester. Copying public fields cannot confer authority.
func (s *Service) StartForRecovery(ctx context.Context, e *jobs.Execution, actor domain.User, guard RecoveryStartGuard) error {
	if guard == nil {
		return domain.E(domain.CodeForbidden, "recovery start requires a current generation guard")
	}
	if err := activeUser(actor, false); err != nil {
		return err
	}
	if err := e.WithInstanceMutation(ctx, func(id string) error {
		var requester int64
		var kind string
		if err := s.db.QueryRowContext(ctx, `SELECT requested_by,kind FROM jobs WHERE instance_id=? AND state='running'`, id).Scan(&requester, &kind); err != nil {
			return domain.E(domain.CodeForbidden, "recovery execution unavailable")
		}
		if kind != "crash_recovery" || requester != actor.ID {
			return domain.E(domain.CodeForbidden, "recovery requester differs from durable execution")
		}
		return s.actor(ctx, requester, false)
	}); err != nil {
		return err
	}
	return s.startGuarded(ctx, e, guard)
}
