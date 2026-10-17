//go:build linux

package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
)

// RestoreRetainedInstalledForRollback restores a captured prior receipt after
// a failed reinstall has returned the canonical manager executable to absence.
// Ordinary receipt publication continues to require the executable to exist.
func (s *LinuxInstaller) RestoreRetainedInstalledForRollback(ctx context.Context, captured *MaintenanceReceipt, expectedCurrent, retained Installed) (retErr error) {
	if s == nil {
		return errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.dataRelease == nil || !s.dataLocked || s.maintenance == nil || s.state == nil || s.bin == nil || s.dataRoot == nil {
		return errors.New("live installer session and stopped manager data lock are required")
	}
	m := s.maintenance
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.release == nil {
		return errors.New("updater operation lock is not held")
	}
	if captured == nil || captured.owner != m || !captured.present {
		return errors.New("captured installed receipt does not belong to this session")
	}
	if !validInstalledReceipt(expectedCurrent) || !validInstalledReceipt(retained) {
		return errors.New("expected or retained installed receipt is invalid")
	}
	if captured.value != expectedCurrent && captured.value != retained {
		return errors.New("captured installed receipt differs from expected or retained image")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.hooks.checkUpdaterLock == nil || s.hooks.checkUpdaterLock(s.state) != nil {
		return errors.New("updater operation lock changed")
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
	if err := m.checkConflicts(ctx, false); err != nil {
		return errors.New("pending updater state requires reconciliation")
	}
	if err := retainedManagerBinaryAbsent(m); err != nil {
		return err
	}
	if err := m.compareCurrentReceipt(ctx, captured); err != nil {
		return errors.New("installed receipt changed since capture")
	}
	if captured.value == retained {
		return nil
	}
	if captured.value != expectedCurrent {
		return errors.New("captured receipt is not the expected current image")
	}
	body, err := json.Marshal(retained)
	if err != nil || len(body) == 0 || len(body) > maintenanceReceiptLimit {
		return errors.Join(errors.New("retained receipt could not be encoded"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	pending, err := m.state.OpenFile("installation-receipt.pending", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return errors.New("pending retained receipt could not be created")
	}
	pendingInfo, err := pending.Stat()
	if err != nil || !pendingInfo.Mode().IsRegular() || pendingInfo.Mode().Perm() != 0600 {
		_ = pending.Close()
		return errors.Join(errors.New("pending retained receipt identity is unsafe"), err)
	}
	cleanupBody := []byte(nil)
	cleanupKnown := false
	published := false
	defer func() {
		if published {
			return
		}
		if !cleanupKnown {
			retErr = errors.Join(retErr, errors.New("pending retained receipt write length is uncertain"))
		} else if cleanupErr := removeExactOwnedRetainedPending(m.state, pending, pendingInfo, cleanupBody, m.hooks.checkProtectedFile); cleanupErr != nil {
			retErr = errors.Join(retErr, errors.New("pending retained receipt cleanup was incomplete"), cleanupErr)
		}
		if closeErr := pending.Close(); closeErr != nil {
			retErr = errors.Join(retErr, errors.New("pending retained receipt close was incomplete"), closeErr)
		}
	}()
	write := m.hooks.writePending
	if write == nil {
		write = writeMaintenancePending
	}
	writeErr := write(ctx, pending, body)
	written, seekErr := pending.Seek(0, io.SeekCurrent)
	if seekErr != nil {
		return errors.Join(errors.New("pending retained receipt write length could not be determined"), writeErr, seekErr)
	}
	if written < 0 || written > int64(len(body)) {
		return errors.Join(errors.New("pending retained receipt write length is invalid"), writeErr)
	}
	cleanupBody = bytes.Clone(body[:int(written)])
	cleanupKnown = true
	if writeErr != nil {
		return errors.Join(errors.New("pending retained receipt write was incomplete"), writeErr)
	}
	if written != int64(len(body)) {
		return errors.New("pending retained receipt write was short")
	}
	if err := pending.Sync(); err != nil {
		return errors.Join(errors.New("pending retained receipt sync failed"), err)
	}
	if err := m.verifyPending(pending, pendingInfo, body); err != nil {
		return errors.Join(errors.New("pending retained receipt identity changed"), err)
	}
	if err := m.hooks.fence(ctx, m.state); err != nil {
		return errors.Join(errors.New("pending retained receipt durability is uncertain"), err)
	}
	if m.hooks.beforePublish != nil {
		if err := m.hooks.beforePublish(ctx, m.state); err != nil {
			return errors.Join(errors.New("pending retained receipt publication was interrupted"), err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.requireStopped(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return errors.New("protected installation root changed before receipt publication")
	}
	if err := s.verifyDataBinding(ctx); err != nil {
		return err
	}
	if s.hooks.checkUpdaterLock(s.state) != nil || m.checkConflicts(ctx, true) != nil {
		return errors.New("updater state changed before receipt publication")
	}
	if err := retainedManagerBinaryAbsent(m); err != nil {
		return err
	}
	if err := m.compareCurrentReceipt(ctx, captured); err != nil {
		return errors.New("installed receipt changed before retained receipt publication")
	}
	if err := m.verifyPending(pending, pendingInfo, body); err != nil {
		return errors.Join(errors.New("pending retained receipt identity changed before publication"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.state.Rename("installation-receipt.pending", "installed.json"); err != nil {
		return errors.Join(errors.New("retained receipt publication failed"), err)
	}
	published = true
	m.pins = append(m.pins, pending)
	if err := m.hooks.fence(ctx, m.state); err != nil {
		return errors.New("retained receipt publication durability is uncertain")
	}
	return nil
}

func retainedManagerBinaryAbsent(m *Maintenance) error {
	if m == nil || m.bin == nil {
		return errors.New("manager binary root is unavailable")
	}
	if _, err := m.bin.Lstat(m.paths.Binary); err == nil {
		return errors.New("canonical manager executable must be absent for retained receipt restoration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("canonical manager executable state is unsafe")
	}
	return nil
}

func removeExactOwnedRetainedPending(root *os.Root, file *os.File, expected os.FileInfo, body []byte, check func(*os.Root, string) error) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := root.Lstat("installation-receipt.pending")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameMaintenanceIdentity(expected, info) || !os.SameFile(expected, pathInfo) {
		return errors.New("foreign pending retained receipt replacement preserved")
	}
	if check == nil {
		return errors.New("pending retained receipt ownership cannot be revalidated")
	}
	if err := check(root, "installation-receipt.pending"); err != nil {
		return err
	}
	raw, err := readMaintenanceFile(context.Background(), file, maintenanceReceiptLimit)
	if err != nil || !bytes.Equal(raw, body) {
		return errors.Join(errors.New("changed pending retained receipt preserved"), err)
	}
	return root.Remove("installation-receipt.pending")
}
