//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"os"
	"time"
)

type capturedMaintenanceControl struct {
	root      *os.Root
	info      os.FileInfo
	challenge *capturedInstallerFile
	drained   *capturedInstallerFile
	uid, gid  int
}

// ReconcileUpdaterForMaintenance reconciles updater-owned terminal files only
// after validating and preserving the manager's maintenance drain proof.
func (s *LinuxInstaller) ReconcileUpdaterForMaintenance(ctx context.Context, snapshot *LinuxUpdaterSnapshot, expected MaintenanceChallenge) error {
	copy := expected
	if expected.From != nil {
		from := *expected.From
		copy.From = &from
	}
	if expected.To != nil {
		to := *expected.To
		copy.To = &to
	}
	return s.reconcileUpdater(ctx, snapshot, &copy)
}

func validateMaintenanceReconcileProof(ctx context.Context, s *LinuxInstaller, snapshot *LinuxUpdaterSnapshot, expected *MaintenanceChallenge) (*capturedMaintenanceControl, error) {
	if expected == nil {
		return nil, nil
	}
	if s == nil || snapshot == nil || !snapshot.present || s.dataRelease == nil || s.maintenance == nil || s.maintenance.release == nil || expected.From == nil || *expected.From != snapshot.installed || expected.From.Schema != snapshot.installed.Schema {
		return nil, errors.New("maintenance challenge source does not match captured installed image")
	}
	if expected.To != nil && expected.To.Schema != expected.From.Schema {
		return nil, errors.New("maintenance challenge schema differs from captured installed image")
	}
	if err := validateMaintenanceChallenge(*expected, time.Now()); err != nil {
		return nil, errors.New("maintenance challenge is expired or invalid")
	}
	if s.hooks.openDataRoot == nil || s.hooks.managerIDs == nil || s.hooks.checkUpdaterLock == nil {
		return nil, errors.New("maintenance control validation is unavailable")
	}
	if err := s.requireStopped(ctx); err != nil {
		return nil, err
	}
	if err := s.verify(ctx); err != nil {
		return nil, errors.New("protected installation root changed")
	}
	if err := s.verifyDataBinding(ctx); err != nil {
		return nil, err
	}
	if err := s.hooks.checkUpdaterLock(s.state); err != nil {
		return nil, errors.New("updater operation lock changed")
	}
	root, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
	if err != nil {
		return nil, errors.New("maintenance control directory is unavailable")
	}
	proof := &capturedMaintenanceControl{root: root}
	fail := func(err error) (*capturedMaintenanceControl, error) {
		_ = proof.close()
		return nil, err
	}
	proof.info, err = root.Stat(".")
	if err != nil {
		return fail(err)
	}
	if snapshot.controlRoot != nil {
		oldInfo, oldErr := snapshot.controlRoot.Stat(".")
		if oldErr != nil || snapshot.controlInfo == nil || !os.SameFile(oldInfo, proof.info) || !os.SameFile(snapshot.controlInfo, proof.info) {
			return fail(errors.New("maintenance control root differs from captured control root"))
		}
	} else if !snapshot.controlAbsent {
		return fail(errors.New("maintenance control root has no captured baseline"))
	}
	proof.uid, proof.gid, err = s.hooks.managerIDs()
	if err != nil {
		return fail(errors.New("manager account is unavailable"))
	}
	check := func(root *os.Root, name string) error {
		if err := checkManagerControlFile(root, name, proof.uid, proof.gid); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			return errors.New("maintenance control file mode is not private 0600")
		}
		return nil
	}
	proof.challenge, err = captureInstallerFile(ctx, root, "maintenance-challenge.json", maintenanceChallengeLimit, check)
	if err != nil {
		return fail(errors.New("maintenance challenge could not be captured"))
	}
	proof.drained, err = captureInstallerFile(ctx, root, "maintenance-drained.json", 2048, check)
	if err != nil {
		return fail(errors.New("maintenance drain receipt could not be captured"))
	}
	var actual MaintenanceChallenge
	var receipt MaintenanceDrainReceipt
	if strictJSON(proof.challenge.raw, &actual) != nil || !sameMaintenanceChallenge(*expected, actual) || validateMaintenanceChallenge(actual, time.Now()) != nil {
		return fail(errors.New("maintenance challenge differs from caller-provided challenge"))
	}
	if strictJSON(proof.drained.raw, &receipt) != nil || validateMaintenanceReceipt(receipt, actual, time.Now()) != nil {
		return fail(errors.New("maintenance drain receipt is invalid"))
	}
	if err := proof.verify(ctx, s, snapshot, false); err != nil {
		return fail(err)
	}
	return proof, nil
}

func (p *capturedMaintenanceControl) verify(ctx context.Context, s *LinuxInstaller, snapshot *LinuxUpdaterSnapshot, afterCleanup bool) error {
	if p == nil || p.root == nil || p.challenge == nil || p.drained == nil {
		return errors.New("maintenance control proof is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.challenge.verify(ctx, func(root *os.Root, name string) error { return checkManagerControlFile(root, name, p.uid, p.gid) }); err != nil {
		return errors.New("maintenance challenge changed")
	}
	if err := p.drained.verify(ctx, func(root *os.Root, name string) error { return checkManagerControlFile(root, name, p.uid, p.gid) }); err != nil {
		return errors.New("maintenance drain receipt changed")
	}
	for _, file := range []*capturedInstallerFile{p.challenge, p.drained} {
		info, err := file.root.Lstat(file.name)
		if err != nil || info.Mode().Perm() != 0600 {
			return errors.New("maintenance control file mode changed")
		}
	}
	current, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
	if err != nil {
		return errors.New("maintenance control root changed")
	}
	defer current.Close()
	currentInfo, currentErr := current.Stat(".")
	proofInfo, proofErr := p.root.Stat(".")
	if currentErr != nil || proofErr != nil || !os.SameFile(currentInfo, proofInfo) || !os.SameFile(p.info, proofInfo) {
		return errors.New("maintenance control root changed")
	}
	if snapshot.controlRoot != nil {
		capturedInfo, capturedErr := snapshot.controlRoot.Stat(".")
		if capturedErr != nil || !os.SameFile(capturedInfo, proofInfo) || snapshot.controlInfo == nil || !os.SameFile(snapshot.controlInfo, proofInfo) {
			return errors.New("maintenance control root differs from snapshot")
		}
	} else if !snapshot.controlAbsent || snapshot.controlInfo != nil {
		return errors.New("maintenance control root transition is not authorized")
	}
	entries, err := installerReadDir(p.root)
	if err != nil {
		return err
	}
	want := map[string]bool{"maintenance-challenge.json": true, "maintenance-drained.json": true}
	if !afterCleanup {
		for name := range snapshot.control {
			want[name] = true
		}
	}
	if len(entries) != len(want) {
		return errors.New("maintenance control file set changed")
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !want[entry.Name()] {
			return errors.New("unrecognized maintenance control file")
		}
	}
	return nil
}

func (p *capturedMaintenanceControl) close() error {
	if p == nil {
		return nil
	}
	var err error
	for _, file := range []*capturedInstallerFile{p.challenge, p.drained} {
		if file != nil && file.file != nil {
			err = errors.Join(err, file.file.Close())
		}
	}
	if p.root != nil {
		err = errors.Join(err, p.root.Close())
	}
	return err
}
