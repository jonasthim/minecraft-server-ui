package scheduler

import (
	"context"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

// BeginUpdateStop mints an update start guard for exactly the stop epoch this
// call commits. Later operator stop/recovery configuration invalidates it. The
// returned integer is diagnostic; only the closure carries start authority.
func (s *Service) BeginUpdateStop(ctx context.Context, e *jobs.Execution, actor domain.User) (epoch int64, guard lifecycle.RecoveryStartGuard, err error) {
	var bound jobs.CurrentExecution
	err = e.WithInstanceMutation(ctx, func(id string) error {
		current, err := e.CheckCurrent(ctx)
		if err != nil {
			return err
		}
		if current.InstanceID != id || current.RequesterID != actor.ID || current.Kind != "instance_update" && current.Kind != "instance_rollback" {
			return domain.E(domain.CodeForbidden, "original update execution required")
		}
		fresh, err := s.actor(ctx, current.RequesterID)
		if err != nil {
			return err
		}
		if current.Kind == "instance_rollback" && fresh.Role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "instance rollback requires administrator authority")
		}
		lock := s.recoveryLock(id)
		lock.Lock()
		defer lock.Unlock()
		epoch, err = s.cancelRecoveryLocked(ctx, id, fresh)
		if err != nil {
			return err
		}
		bound = current
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	var consumed atomic.Bool
	guard = func(caller context.Context, id string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
		if start == nil || id != bound.InstanceID {
			return domain.ProcessIdentity{}, domain.E(domain.CodeForbidden, "update start guard identity differs")
		}
		if err := ctx.Err(); err != nil {
			return domain.ProcessIdentity{}, err
		}
		current, err := e.CheckCurrent(caller)
		if err != nil {
			return domain.ProcessIdentity{}, err
		}
		if current != bound {
			return domain.ProcessIdentity{}, domain.E(domain.CodeForbidden, "update start execution changed")
		}
		if !consumed.CompareAndSwap(false, true) {
			return domain.ProcessIdentity{}, domain.E(domain.CodeForbidden, "update start guard was consumed")
		}
		operation, cancel := context.WithCancel(caller)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		lock := s.recoveryLock(id)
		lock.Lock()
		defer lock.Unlock()
		if err = operation.Err(); err != nil {
			return domain.ProcessIdentity{}, err
		}
		if _, err = s.actor(operation, bound.RequesterID); err != nil {
			return domain.ProcessIdentity{}, err
		}
		var actual int64
		if err = s.db.QueryRowContext(operation, "SELECT epoch FROM crash_recovery WHERE instance_id=?", id).Scan(&actual); err != nil {
			return domain.ProcessIdentity{}, err
		}
		if actual != epoch {
			return domain.ProcessIdentity{}, domain.E(domain.CodeConflict, "a later deliberate stop or recovery configuration prevents update restart")
		}
		if _, err = e.CheckCurrent(operation); err != nil {
			return domain.ProcessIdentity{}, err
		}
		return start()
	}
	return epoch, guard, nil
}
