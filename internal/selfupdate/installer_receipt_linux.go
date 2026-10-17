//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"os"
)

// RemoveInstalledForRollback removes only the fixed installed receipt captured
// by this Maintenance session. A captured absence is an idempotent no-op.
func (s *LinuxInstaller) RemoveInstalledForRollback(ctx context.Context, prior *MaintenanceReceipt, expected Installed) error {
	if s == nil {
		return errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.dataLocked || s.dataRelease == nil || s.maintenance == nil || s.state == nil || s.bin == nil || s.dataRoot == nil {
		return errors.New("live installer session and stopped manager data lock are required")
	}
	m := s.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("maintenance session is closed")
	}
	if m.release == nil {
		return errors.New("updater operation lock is not held")
	}
	if prior == nil || prior.owner != m {
		return errors.New("captured receipt does not belong to this installer session")
	}
	if !validInstalledReceipt(expected) {
		return errors.New("expected installed receipt is invalid")
	}
	if prior.present && prior.value != expected {
		return errors.New("captured installed receipt differs from expected image")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.requireStopped(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return errors.New("protected installation root changed")
	}
	if err := s.verifyDataBinding(ctx); err != nil {
		return err
	}
	if s.hooks.checkUpdaterLock == nil || s.hooks.checkUpdaterLock(s.state) != nil {
		return errors.New("updater operation lock changed")
	}
	if err := m.checkConflicts(ctx, false); err != nil {
		return errors.New("pending updater state requires reconciliation")
	}
	if prior.present {
		if err := m.verifyExecutable(ctx, expected); err != nil {
			return errors.New("canonical manager image differs from expected receipt")
		}
	}
	// This comparison binds the fixed receipt path to both the captured file
	// identity and bytes, or confirms absence, immediately before unlinking.
	if err := m.compareCurrentReceipt(ctx, prior); err != nil {
		return errors.New("installed receipt changed since capture")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !prior.present {
		return nil
	}
	if err := s.requireStopped(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return errors.New("protected installation root changed before receipt removal")
	}
	if err := s.verifyDataBinding(ctx); err != nil {
		return err
	}
	if err := m.checkConflicts(ctx, false); err != nil {
		return errors.New("pending updater state appeared before receipt removal")
	}
	if err := m.compareCurrentReceipt(ctx, prior); err != nil {
		return errors.New("installed receipt changed before removal")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.state.Remove("installed.json"); err != nil {
		return errors.New("installed receipt removal failed or is uncertain")
	}
	fence := s.hooks.fence
	if fence == nil {
		fence = syncPrivilegedRoot
	}
	if err := fence(ctx, m.state); err != nil {
		return errors.New("installed receipt removal durability is uncertain")
	}
	return nil
}

func (s *LinuxInstaller) verifyDataBinding(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := s.hooks.openDataRoot(ctx, s.maintenance.paths.Data)
	if err != nil {
		return errors.New("manager data directory binding changed")
	}
	defer current.Close()
	heldInfo, heldErr := s.dataRoot.Stat(".")
	currentInfo, currentErr := current.Stat(".")
	if heldErr != nil || currentErr != nil || !os.SameFile(heldInfo, currentInfo) {
		return errors.New("manager data directory binding changed")
	}
	uid, gid, err := s.hooks.managerIDs()
	if err != nil || checkManagerDataRoot(s.dataRoot, uid, gid) != nil {
		return errors.New("manager data directory ownership changed")
	}
	if err = checkManagerLock(s.dataRoot, uid, gid); err != nil {
		return errors.New("manager data lock changed")
	}
	return nil
}
