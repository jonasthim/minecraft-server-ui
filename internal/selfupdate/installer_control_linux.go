//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"os"
	"strings"
)

// PrepareMaintenanceControl removes only ordinary updater control files that
// were captured as part of a valid terminal snapshot. The manager may still be
// running, so updater journal, receipt, executable, scratch files, and
// privileged request state remain untouched for the later stopped reconcile.
func (s *LinuxInstaller) PrepareMaintenanceControl(ctx context.Context, snapshot *LinuxUpdaterSnapshot) (*LinuxUpdaterSnapshot, error) {
	if s == nil || ctx == nil {
		return nil, errors.New("installer session unavailable")
	}
	s.mu.Lock()
	if s.closed || s.maintenance == nil || s.maintenance.release == nil || s.state == nil || s.bin == nil {
		s.mu.Unlock()
		return nil, errors.New("installer session is closed")
	}
	if _, _, err := s.validateSnapshot(snapshot); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := s.verify(ctx); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.hooks.checkUpdaterLock == nil || s.hooks.checkUpdaterLock(s.state) != nil {
		s.mu.Unlock()
		return nil, errors.New("updater operation lock is unavailable or changed")
	}
	if !snapshot.terminal {
		s.mu.Unlock()
		return snapshot, nil
	}
	if err := verifyTerminalSnapshotForControlMutation(ctx, s, snapshot, nil); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if len(snapshot.control) == 0 {
		s.mu.Unlock()
		return snapshot, nil
	}
	if snapshot.controlRoot == nil {
		s.mu.Unlock()
		return nil, errors.New("captured manager control root is unavailable")
	}
	removed := make(map[string]bool, len(snapshot.control))
	for _, name := range []string{"request.json", "drained.json", "challenge.json"} {
		file := snapshot.control[name]
		if file == nil {
			continue
		}
		if err := verifyControlSet(ctx, s, snapshot, removed); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		uid, gid, err := s.hooks.managerIDs()
		if err != nil {
			s.mu.Unlock()
			return nil, errors.New("manager account identity is unavailable")
		}
		check := func(root *os.Root, name string) error { return checkManagerControlFile(root, name, uid, gid) }
		if err := file.verify(ctx, check); err != nil {
			s.mu.Unlock()
			return nil, errors.New("captured manager control file changed")
		}
		pathInfo, err := snapshot.controlRoot.Lstat(name)
		if err != nil || !os.SameFile(pathInfo, file.info) {
			s.mu.Unlock()
			return nil, errors.Join(errors.New("manager control path changed before removal"), err)
		}
		remove := s.hooks.remove
		if remove == nil {
			remove = func(root *os.Root, name string) error { return root.Remove(name) }
		}
		if err := remove(snapshot.controlRoot, name); err != nil {
			s.mu.Unlock()
			return nil, err
		}
		removed[name] = true
	}
	fence := s.hooks.fence
	if fence == nil {
		fence = syncPrivilegedRoot
	}
	if err := fence(ctx, snapshot.controlRoot); err != nil {
		s.mu.Unlock()
		return nil, errors.Join(errors.New("sync prepared maintenance control directory"), err)
	}
	if err := verifyControlSet(ctx, s, snapshot, removed); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if err := verifyTerminalSnapshotForControlMutation(ctx, s, snapshot, removed); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	for name := range snapshot.control {
		if _, err := snapshot.controlRoot.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			s.mu.Unlock()
			return nil, errors.Join(errors.New("captured manager control file remains after removal"), err)
		}
	}
	if err := snapshot.close(); err != nil {
		s.mu.Unlock()
		return nil, errors.Join(errors.New("close pre-maintenance updater snapshot"), err)
	}
	s.mu.Unlock()

	// Re-capture after owned controls are gone. This is the snapshot later
	// reconciled under the stopped-manager data lock with the installer proof.
	fresh, err := s.SnapshotUpdater(ctx)
	if err != nil {
		return nil, errors.Join(errors.New("recapture terminal updater state after control cleanup"), err)
	}
	if !fresh.terminal || fresh.installed != snapshot.installed {
		_ = fresh.close()
		return nil, errors.New("updater state changed while preparing installer maintenance")
	}
	return fresh, nil
}

func verifyTerminalSnapshotForControlMutation(ctx context.Context, s *LinuxInstaller, snapshot *LinuxUpdaterSnapshot, removed map[string]bool) error {
	if err := s.verify(ctx); err != nil {
		return err
	}
	if s.hooks.checkUpdaterLock == nil || s.hooks.checkUpdaterLock(s.state) != nil {
		return errors.New("updater operation lock changed")
	}
	if snapshot.journal == nil || snapshot.receipt == nil || !snapshot.receipt.present {
		return errors.New("terminal updater snapshot is incomplete")
	}
	if err := snapshot.journal.verify(ctx, s.hooks.checkProtectedFile); err != nil {
		return errors.New("captured terminal activation journal changed")
	}
	var journal activationJournal
	if strictJSON(snapshot.journal.raw, &journal) != nil || !terminalLinuxPhase(journal.Phase) {
		return errors.New("captured updater journal is not terminal")
	}
	if err := validateTerminalRequest(snapshot, journal); err != nil {
		return err
	}
	for _, item := range []struct {
		root *os.Root
		name string
		file *capturedInstallerFile
	}{{s.state, "request.json", snapshot.request}, {s.bin, s.maintenance.paths.Binary + ".previous", snapshot.previous}, {s.bin, s.maintenance.paths.Binary + ".next", snapshot.next}} {
		file := item.file
		if file == nil {
			if _, err := item.root.Lstat(item.name); !errors.Is(err, os.ErrNotExist) {
				return errors.Join(errors.New("uncaptured updater request or scratch image appeared"), err)
			}
			continue
		}
		if err := file.verify(ctx, s.hooks.checkProtectedFile); err != nil {
			return errors.New("captured updater request or scratch image changed")
		}
	}
	entries, err := installerReadDir(s.bin)
	if err != nil {
		return errors.New("manager binary directory cannot be revalidated")
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, s.maintenance.paths.Binary+".") && name != s.maintenance.paths.Binary+".previous" && name != s.maintenance.paths.Binary+".next" {
			return errors.New("unknown manager scratch file requires review")
		}
	}
	if err := validateTerminalScratch(snapshot, journal); err != nil {
		return err
	}
	if err := s.maintenance.compareCurrentReceipt(ctx, snapshot.receipt); err != nil || snapshot.receipt.value != snapshot.installed {
		return errors.Join(errors.New("captured installed receipt changed"), err)
	}
	if err := s.maintenance.verifyExecutable(ctx, snapshot.installed); err != nil {
		return errors.New("captured manager executable changed")
	}
	for _, name := range []string{"activation.pending", "launch.json", "launch.pending", "installed.pending", "installation-receipt.pending"} {
		if _, err := s.state.Lstat(name); err == nil {
			return errors.New("pending updater state requires recovery")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if snapshot.controlRoot == nil {
		if !snapshot.controlAbsent || len(snapshot.control) != 0 {
			return errors.New("captured manager control root is unavailable")
		}
		root, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
		if err == nil {
			_ = root.Close()
			return errors.New("manager control root appeared after capture")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("manager control root cannot be revalidated")
		}
		return nil
	}
	return verifyControlSet(ctx, s, snapshot, removed)
}

func verifyControlSet(ctx context.Context, s *LinuxInstaller, snapshot *LinuxUpdaterSnapshot, removed map[string]bool) error {
	current, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
	if err != nil {
		return errors.New("manager control root changed")
	}
	defer current.Close()
	oldInfo, oldErr := snapshot.controlRoot.Stat(".")
	newInfo, newErr := current.Stat(".")
	if oldErr != nil || newErr != nil || snapshot.controlInfo == nil || !os.SameFile(snapshot.controlInfo, oldInfo) || !os.SameFile(oldInfo, newInfo) {
		return errors.New("manager control root identity changed")
	}
	entries, err := installerReadDir(snapshot.controlRoot)
	if err != nil {
		return err
	}
	want := 0
	for name, file := range snapshot.control {
		if file == nil || removed[name] {
			continue
		}
		want++
	}
	if len(entries) != want {
		return errors.New("manager control file set changed")
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		file := snapshot.control[name]
		if file == nil || removed[name] || seen[name] || !entry.Type().IsRegular() {
			return errors.New("foreign or replaced manager control entry appeared")
		}
		seen[name] = true
	}
	for name, file := range snapshot.control {
		if file == nil || removed[name] {
			continue
		}
		uid, gid, err := s.hooks.managerIDs()
		if err != nil {
			return errors.New("manager account identity is unavailable")
		}
		if err := file.verify(ctx, func(root *os.Root, name string) error { return checkManagerControlFile(root, name, uid, gid) }); err != nil {
			return errors.New("captured manager control entry changed")
		}
	}
	return nil
}
