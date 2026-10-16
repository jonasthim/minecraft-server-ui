package console

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"time"
)

// RecoverSaving is restricted to paused startup admission and exact persisted
// process identity. It repairs saving only, allowing an old WatchdogError while
// retaining every liveness/generation guard. A Verified result with an error
// means saving was freshly acknowledged but old lease clearance is unresolved;
// callers must not admit another lease until its original expiry has passed.
func (s *Service) RecoverSaving(ctx context.Context, identity domain.ProcessIdentity, leaseID string) (result domain.CommandResult, err error) {
	if s.queue == nil || s.native == nil || s.instances == nil || leaseID == "" || len(leaseID) > 128 {
		return result, domain.E(domain.CodeUnavailable, "saving recovery is unavailable")
	}
	operation, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = s.queue.WithPausedInstance(operation, identity.InstanceID, func() error {
		unlock, e := s.lock(operation, identity.InstanceID)
		if e != nil {
			return e
		}
		defer unlock()
		check := func() error {
			current, e := s.native.Identity(operation, identity.InstanceID)
			if e != nil || current != identity {
				return domain.E(domain.CodeUnavailable, "saving recovery JVM identity changed")
			}
			observed, e := s.native.Inspect(operation, identity)
			if e != nil || !observed.ProcessRunning || observed.State != domain.StateRunning {
				return domain.E(domain.CodeUnavailable, "saving recovery JVM is not running")
			}
			return operation.Err()
		}
		if e := check(); e != nil {
			return e
		}
		detail, e := s.instances.Get(operation, identity.InstanceID)
		if e != nil {
			return e
		}
		cursor, cursorErr := newRawCursor(operation, s.paths, identity.InstanceID)
		releaseErr := s.native.ReleaseSaveLease(operation, identity, leaseID)
		if releaseErr != nil {
			if e := check(); e != nil {
				return e
			}
			if e := s.native.Command(operation, identity, "save-on"); e != nil {
				return domain.E(domain.CodeUnavailable, "saving recovery could not deliver save-on")
			}
		}
		result.Received = true
		if cursorErr != nil {
			return domain.E(domain.CodeUnavailable, "save-on attempted but fresh acknowledgement cursor unavailable")
		}
		out, e := cursor.wait(operation, ackSaveOn(detail.Instance.MinecraftVersion))
		if e != nil {
			return e
		}
		if e := check(); e != nil {
			return e
		}
		result.Verified = true
		result.Output = Redact(out, nil)
		if releaseErr != nil {
			return domain.E(domain.CodeUnavailable, "saving enabled but old native lease clearance is unresolved")
		}
		return nil
	})
	return result, err
}

// ReleaseSavingForBackup retains the original live callback's cleanup authority
// and attempts a fresh save-on even when the old lease was already cleared by
// its watchdog. A fallback acknowledgement does not prove old lease clearance.
func (s *Session) ReleaseSavingForBackup(ctx context.Context, leaseID string) (domain.CommandResult, error) {
	result, releaseErr := s.ReleaseSaving(ctx, leaseID)
	if releaseErr == nil && result.Verified {
		return result, nil
	}
	scope, unlock, err := s.enter(ctx, true)
	if err != nil {
		return result, err
	}
	defer unlock()
	operation, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	check := func() error {
		identity, err := scope.service.native.Identity(operation, scope.identity.InstanceID)
		if err != nil || identity != scope.identity {
			return domain.E(domain.CodeUnavailable, "saving cleanup JVM identity changed")
		}
		observed, err := scope.service.native.Inspect(operation, identity)
		if err != nil || !observed.ProcessRunning || observed.State != domain.StateRunning {
			return domain.E(domain.CodeUnavailable, "saving cleanup JVM is unavailable")
		}
		return operation.Err()
	}
	if err = check(); err != nil {
		return result, err
	}
	cursor, cursorErr := newRawCursor(operation, scope.service.paths, scope.identity.InstanceID)
	if err = scope.service.native.Command(operation, scope.identity, "save-on"); err != nil {
		return result, domain.E(domain.CodeUnavailable, "saving cleanup could not deliver save-on")
	}
	result.Received = true
	if cursorErr != nil {
		return result, domain.E(domain.CodeUnavailable, "save-on attempted but cleanup cursor is unavailable")
	}
	out, err := cursor.wait(operation, ackSaveOn(scope.detail.Instance.MinecraftVersion))
	if err != nil {
		return result, err
	}
	if err = check(); err != nil {
		return result, err
	}
	result.Output = Redact(out, nil)
	result.Verified = true
	return result, domain.E(domain.CodeUnavailable, "saving acknowledged but previous lease release could not be confirmed")
}
