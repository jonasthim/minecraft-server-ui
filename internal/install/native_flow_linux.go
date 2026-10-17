//go:build linux

package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/internal/installinputs"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func (d *linuxInstallDriver) Quiesce(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	if tx.Mode == modeApply && d.inputs == nil {
		if err := d.reopenAuthenticatedInputs(ctx, tx, intent); err != nil {
			return err
		}
	}
	if d.updater == nil {
		d.updater, err = d.session.SnapshotUpdater(ctx)
		if err != nil {
			return errors.New("updater state changed before manager quiescence")
		}
	}
	if d.updater.HasTerminal() {
		preparer, ok := d.session.(linuxMaintenanceUpdaterPreparer)
		if !ok {
			return errors.New("Linux updater bridge does not support terminal maintenance preparation")
		}
		d.updater, err = preparer.PrepareMaintenanceControl(ctx, d.updater)
		if err != nil {
			return fmt.Errorf("prepare terminal updater controls for maintenance: %w", err)
		}
	}
	if tx.Mode == modeUninstall {
		gamesStopped := d.hooks.gamesStopped
		if gamesStopped == nil {
			gamesStopped = requireLinuxGamesStopped
		}
		if err := gamesStopped(ctx); err != nil {
			return err
		}
	}
	principal, created, err := d.evidencePrincipal(ctx)
	if err != nil {
		return err
	}
	if principal == nil {
		value, e := d.provisionAccountAndData(ctx, intent)
		if e != nil {
			return e
		}
		principal = &value
		created = true
	} else {
		current, present, e := d.inspectAccount(ctx)
		if e != nil || !present || !sameLinuxPrincipal(*principal, current) {
			return errors.New("persisted manager account identity no longer matches host")
		}
		if e := ensureLinuxDataRoot(ctx, d.hooks.dataRoot, *principal, false); e != nil {
			return e
		}
	}
	if created && intent.Principal != nil {
		return errors.New("created account evidence conflicts with prior installation")
	}
	service, err := d.inspectService(ctx)
	if err != nil {
		return errors.New("manager service state is unavailable")
	}
	challengeWritten := false
	if serviceActive(service) {
		if intent.Prior == nil || !intent.ServiceWasActive {
			return errors.New("active manager is not owned by the captured installation")
		}
		if err := d.writeMaintenanceChallenge(ctx, tx, intent, *principal); err != nil {
			return err
		}
		challengeWritten = true
		stop := d.hooks.stop
		if stop == nil {
			stop = stopLinuxManager
		}
		if err := stop(ctx); err != nil {
			return errors.New("manager service could not be stopped cleanly")
		}
	}
	if err := d.lockStoppedManager(ctx); err != nil {
		return err
	}
	if err := ensureLinuxDataDirectories(ctx, d.hooks.dataRoot, *principal, false); err != nil {
		return err
	}
	if tx.Mode == modeApply && !challengeWritten {
		if err := d.writeMaintenanceChallenge(ctx, tx, intent, *principal); err != nil {
			return err
		}
	}
	maintenanceChallenge := (*selfupdate.MaintenanceChallenge)(nil)
	if intent.ServiceWasActive {
		challenge, _, err := d.readMaintenanceProof(ctx, intent, *principal)
		if err != nil {
			return err
		}
		if intent.Prior == nil || challenge.From == nil {
			return errors.New("maintenance drain is missing its prior image")
		}
		if err := d.probeSchema(ctx, linuxManagerExecutablePath, *principal, intent.Prior.Schema); err != nil {
			return err
		}
		maintenanceChallenge = &challenge
	} else if intent.Prior != nil && intent.PriorInventory != nil && intent.PriorInventory.Status == "installed" {
		if err := d.probeSchema(ctx, linuxManagerExecutablePath, *principal, intent.Prior.Schema); err != nil {
			return err
		}
	} else if intent.Prior != nil && intent.PriorInventory != nil && intent.PriorInventory.Status == "uninstalled" {
		schema, err := selfupdate.ReadOnlySchema(ctx, filepath.Join(d.hooks.dataRoot, "manager.db"))
		if err != nil || schema != intent.Prior.Schema {
			return errors.New("retained manager database schema is missing or inconsistent")
		}
	} else if intent.PriorInventory != nil && intent.PriorInventory.Status == "retained" {
		// The attempted image in retained inventory is not proof of the database
		// schema. A clean failed install may not have created the database; if it
		// exists, check its schema read-only before any retry can start.
		dbPath := filepath.Join(d.hooks.dataRoot, "manager.db")
		if _, statErr := os.Lstat(dbPath); statErr == nil {
			schema, schemaErr := selfupdate.ReadOnlySchema(ctx, dbPath)
			if schemaErr != nil || intent.Target == nil || schema != intent.Target.Schema {
				return errors.New("retained manager database schema is missing or inconsistent")
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("retained manager database path is unsafe")
		}
	} else if err := d.requireFreshOrRetainedEmptyDB(ctx, intent); err != nil {
		return err
	}
	if maintenanceChallenge != nil {
		reconciler, ok := d.session.(linuxMaintenanceUpdaterReconciler)
		if !ok {
			return errors.New("Linux updater bridge does not support exact installer maintenance reconciliation")
		}
		if err := reconciler.ReconcileUpdaterForMaintenance(ctx, d.updater, *maintenanceChallenge); err != nil {
			return fmt.Errorf("reconcile terminal manager updater state during maintenance: %w", err)
		}
	} else if err := d.session.ReconcileUpdater(ctx, d.updater); err != nil {
		return fmt.Errorf("reconcile terminal manager updater state: %w", err)
	}
	if err := d.markUpdaterReconciled(ctx); err != nil {
		return fmt.Errorf("persist updater reconciliation proof: %w", err)
	}
	return nil
}

func (d *linuxInstallDriver) reopenAuthenticatedInputs(ctx context.Context, tx transaction, intent linuxIntent) error {
	if d.operation == nil || intent.Target == nil || intent.Mode != modeApply || validateLinuxIntent(intent, tx) != nil {
		return errors.New("prepared authenticated inputs are unavailable for apply")
	}
	trust, err := candidate.ParsePublicTrust(intent.BootstrapTrust, "jonasthim/minecraft-server-ui")
	if err != nil {
		return errors.New("recorded independent bootstrap trust is invalid")
	}
	recorded := installinputs.RecordedOptions{
		Version: intent.Target.Version, Trust: trust, AllowDevelopment: intent.AllowDevelopment, ManifestSHA256: intent.ManifestSHA256,
	}
	var inputs linuxInstallerInputs
	if d.hooks.openRecorded != nil {
		inputs, err = d.hooks.openRecorded(ctx, d.operation, recorded)
	} else {
		inputs, err = installinputs.OpenRecorded(ctx, d.operation, recorded)
	}
	if err != nil {
		return fmt.Errorf("reauthenticate protected installer input snapshot: %w", err)
	}
	summary := inputs.Summary()
	if summary.Version != intent.Target.Version || summary.Schema != intent.Target.Schema || summary.ManifestSHA256 != intent.ManifestSHA256 {
		_ = inputs.Close()
		return errors.New("protected installer input summary differs from immutable intent")
	}
	members := make(map[string]candidate.Member)
	for _, member := range inputs.Members() {
		members[member.Name] = member
	}
	for _, spec := range fixedLinuxAssets {
		if spec.Member == "" {
			continue
		}
		var plan *linuxAssetPlan
		for i := range intent.Assets {
			if intent.Assets[i].Role == spec.Role {
				plan = &intent.Assets[i]
				break
			}
		}
		member, ok := members[spec.Member]
		if plan == nil || !ok || !plan.After.Present || member.SHA256 != plan.After.SHA256 || member.Size != plan.After.Size || member.Mode != plan.After.Mode {
			_ = inputs.Close()
			return errors.New("reauthenticated member set differs from immutable asset plan")
		}
	}
	d.inputs = inputs
	return nil
}

func (d *linuxInstallDriver) requireFreshOrRetainedEmptyDB(ctx context.Context, intent linuxIntent) error {
	dbPath := filepath.Join(d.hooks.dataRoot, "manager.db")
	info, err := os.Lstat(dbPath)
	if errors.Is(err, os.ErrNotExist) {
		if intent.Mode == modeApply && intent.Prior == nil {
			return nil
		}
		return errors.New("retained manager database is missing")
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("manager database path is unsafe")
	}
	return errors.New("manager database exists without a matching installed image proof")
}

func (d *linuxInstallDriver) lockStoppedManager(ctx context.Context) error {
	if d.dataHeld {
		return nil
	}
	if err := d.session.LockStoppedManager(ctx); err != nil {
		return fmt.Errorf("acquire stopped manager data lock: %w", err)
	}
	d.dataHeld = true
	return nil
}

func (d *linuxInstallDriver) Replace(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	if !d.dataHeld {
		return errors.New("stopped manager data lock is not held")
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil || principal == nil {
		return errors.New("manager account evidence is unavailable")
	}
	if tx.Mode == modeUninstall {
		gamesStopped := d.hooks.gamesStopped
		if gamesStopped == nil {
			gamesStopped = requireLinuxGamesStopped
		}
		if err := gamesStopped(ctx); err != nil {
			return err
		}
		enabled := d.hooks.enabled
		if enabled == nil {
			enabled = setLinuxManagerEnabled
		}
		if err := enabled(ctx, false); err != nil {
			return fmt.Errorf("disable manager service before uninstall: %w", err)
		}
	}
	if err := d.ensureAssetParents(ctx, intent); err != nil {
		return err
	}
	for _, plan := range intent.Assets {
		if err := ctx.Err(); err != nil {
			return err
		}
		snapshot, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		if snapshot.State() != plan.Before {
			_ = snapshot.Close()
			return fmt.Errorf("deployment asset %s changed before publication", plan.Role)
		}
		_ = d.replaceAssetSnapshot(plan.Role, snapshot)
		// A freshly launched worker has no parent-process asset handles. Bind
		// unchanged roles too, after creating any missing parent directories;
		// Publish validates all roles, including absent optional updater trust.
		if sameLinuxAssetContent(plan.Before, plan.After) {
			continue
		}
		if plan.After.Present {
			source, err := openLinuxAssetSource(ctx, d.inputs, plan.Role, intent.BootstrapTrust, d.operation, intent)
			if err != nil {
				return err
			}
			actual, publishErr := publishLinuxAssetRecorded(ctx, tx.ID, snapshot, source, plan.After, func(staged linuxAssetState) error {
				return d.recordEffect(ctx, plan.Role, "publish", plan.Before, staged)
			})
			closeErr := source.Close()
			if publishErr != nil || closeErr != nil {
				return errors.Join(fmt.Errorf("publish deployment asset %s", plan.Role), publishErr, closeErr)
			}
			if err := d.completeEffect(ctx, plan.Role, "publish", actual); err != nil {
				return err
			}
		} else {
			if err := d.recordEffect(ctx, plan.Role, "remove", plan.Before, linuxAssetState{}); err != nil {
				return err
			}
			if err := removeLinuxAsset(ctx, snapshot); err != nil {
				return err
			}
			if err := d.completeEffect(ctx, plan.Role, "remove", linuxAssetState{}); err != nil {
				return err
			}
		}
		fresh, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		if !matchesPlannedLinuxAsset(fresh.State(), plan.After) {
			_ = fresh.Close()
			return errors.New("published deployment asset does not match immutable intent")
		}
		_ = d.replaceAssetSnapshot(plan.Role, fresh)
	}
	if tx.Mode == modeUninstall {
		gamesStopped := d.hooks.gamesStopped
		if gamesStopped == nil {
			gamesStopped = requireLinuxGamesStopped
		}
		if err := gamesStopped(ctx); err != nil {
			return err
		}
		if err := d.reload(ctx); err != nil {
			return err
		}
	} else if err := d.reload(ctx); err != nil {
		return err
	}
	return nil
}

func (d *linuxInstallDriver) replaceAssetSnapshot(role linuxAssetRole, next *linuxAssetSnapshot) error {
	if current := d.assets[role]; current != nil {
		_ = current.Close()
	}
	d.assets[role] = next
	return nil
}

func (d *linuxInstallDriver) ensureAssetParents(ctx context.Context, intent linuxIntent) error {
	for _, spec := range fixedLinuxAssets {
		if err := ctx.Err(); err != nil {
			return err
		}
		path, boundary, err := linuxAssetPath(spec)
		if err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if _, err := os.Lstat(parent); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := createLinuxAssetParents(ctx, parent, boundary); err != nil {
			return err
		}
		if err := checkLinuxAssetDirectoryChain(parent, boundary); err != nil {
			return err
		}
	}
	return nil
}

func (d *linuxInstallDriver) reload(ctx context.Context) error {
	if d.hooks.reload != nil {
		return d.hooks.reload(ctx)
	}
	return reloadLinuxUnits(ctx)
}

func (d *linuxInstallDriver) Publish(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	if !d.dataHeld {
		return errors.New("stopped manager data lock is not held")
	}
	if tx.Mode == modeApply {
		if intent.Target == nil {
			return errors.New("installer target image is unavailable")
		}
		fresh, err := d.session.Receipt(ctx)
		if err != nil {
			return err
		}
		if err := d.session.CompareAndSwapInstalled(ctx, fresh, toInstalled(*intent.Target)); err != nil {
			return fmt.Errorf("publish installed manager receipt: %w", err)
		}
	}
	status := "installed"
	var image Image
	if tx.Mode == modeUninstall {
		status = "uninstalled"
		image = *intent.Prior
	} else {
		if intent.Target == nil {
			return errors.New("installer target image is unavailable")
		}
		image = *intent.Target
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil || principal == nil {
		return errors.New("manager account evidence is unavailable")
	}
	assets := make([]linuxOwnedAsset, 0, len(intent.Assets))
	for _, plan := range intent.Assets {
		current, err := currentLinuxAsset(ctx, d.assets[plan.Role])
		if err != nil || !matchesPlannedLinuxAsset(current, plan.After) {
			return fmt.Errorf("deployment asset %s changed before inventory publication", plan.Role)
		}
		assets = append(assets, linuxOwnedAsset{Role: plan.Role, State: current})
	}
	inventory := linuxInventory{Format: 1, Status: status, OperationID: tx.ID, Image: image, Principal: *principal, Assets: assets}
	if err := d.publishInventory(ctx, inventory, intent.PriorInventory); err != nil {
		return err
	}
	return nil
}

func matchesPlannedLinuxAsset(actual, planned linuxAssetState) bool {
	if !sameLinuxAssetContent(actual, planned) {
		return false
	}
	if planned.Present && (planned.Device != 0 || planned.Inode != 0) {
		return actual.Device == planned.Device && actual.Inode == planned.Inode
	}
	return true
}

func toInstalled(image Image) selfupdate.Installed {
	return selfupdate.Installed{Format: 1, Version: image.Version, Schema: image.Schema, SHA256: image.SHA256}
}

func (d *linuxInstallDriver) publishInventory(ctx context.Context, next linuxInventory, prior *linuxInventory) error {
	body, err := marshalLinuxInventory(next)
	if err != nil {
		return err
	}
	var priorInfo os.FileInfo
	if prior == nil {
		if _, err := d.state.Lstat("installation.json"); err == nil {
			return errors.New("Linux inventory appeared before publication")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		current, present, err := d.readInventory(ctx)
		if err != nil || !present {
			return errors.Join(errors.New("Linux inventory disappeared before publication"), err)
		}
		if !reflect.DeepEqual(current, *prior) {
			return errors.New("Linux inventory changed before publication")
		}
		priorInfo, err = d.state.Lstat("installation.json")
		if err != nil {
			return err
		}
	}
	return writeLinuxProtectedState(ctx, d.state, "installation.json", body, priorInfo, d.hooks.testRoot)
}

func writeLinuxProtectedState(ctx context.Context, root *os.Root, name string, body []byte, prior os.FileInfo, testRoot bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pending := name + ".pending"
	if _, err := root.Lstat(pending); err == nil {
		return errors.New("protected state pending slot is occupied")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := root.Lstat(name)
	if prior == nil {
		if err == nil {
			return errors.New("protected state appeared before publication")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err != nil || !os.SameFile(prior, current) {
		return errors.New("protected state changed before publication")
	}
	file, err := root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	created, statErr := file.Stat()
	if statErr != nil || !linuxNativeProtectedFile(created, testRoot) || created.Mode().Perm() != 0600 {
		_ = file.Close()
		return errors.Join(errors.New("new protected state file is unsafe"), statErr)
	}
	writeErr := writeAll(file, body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err = root.Lstat(name)
	if prior == nil {
		if err == nil {
			return errors.New("protected state appeared before rename")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if err != nil || !os.SameFile(prior, current) {
		return errors.New("protected state changed before rename")
	}
	pendingInfo, err := root.Lstat(pending)
	if err != nil || !os.SameFile(created, pendingInfo) {
		return errors.Join(errors.New("protected state pending identity changed"), err)
	}
	if err := root.Rename(pending, name); err != nil {
		return fmt.Errorf("publish protected state: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	final, err := root.Lstat(name)
	if err != nil || !os.SameFile(created, final) {
		return errors.Join(errors.New("protected state publication is uncertain"), err)
	}
	return nil
}

func (d *linuxInstallDriver) Verify(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil || principal == nil {
		return errors.New("manager account evidence is unavailable")
	}
	if tx.Mode == modeUninstall {
		gamesStopped := d.hooks.gamesStopped
		if gamesStopped == nil {
			gamesStopped = requireLinuxGamesStopped
		}
		if err := gamesStopped(ctx); err != nil {
			return err
		}
		if err := d.verifyUninstalled(ctx, intent, *principal); err != nil {
			return err
		}
		if err := d.clearOwnMaintenanceControl(ctx, intent, *principal); err != nil {
			return err
		}
		return nil
	}
	if !d.dataHeld {
		return errors.New("stopped manager data lock was released unexpectedly")
	}
	if err := d.probeCandidate(ctx, *principal, *intent.Target); err != nil {
		return err
	}
	if err := d.grantLaunchPermit(ctx, tx, *intent.Target); err != nil {
		return fmt.Errorf("authorize native manager launch: %w", err)
	}
	if err := d.session.UnlockManager(); err != nil {
		d.dataHeld = false
		return fmt.Errorf("release manager data lock for start: %w", err)
	}
	d.dataHeld = false
	start := d.hooks.start
	if start == nil {
		start = startLinuxManager
	}
	if err := start(ctx); err != nil {
		return fmt.Errorf("start installed manager: %w", err)
	}
	if err := d.waitMaintenanceReady(ctx, tx, intent); err != nil {
		return err
	}
	if err := d.revokeLaunchPermit(ctx, tx); err != nil {
		return fmt.Errorf("revoke native manager launch permit: %w", err)
	}
	enabled := d.hooks.enabled
	if enabled == nil {
		enabled = setLinuxManagerEnabled
	}
	wantEnabled := intent.ServiceWasEnabled || intent.Prior == nil || intent.PriorInventory != nil && intent.PriorInventory.Status == "uninstalled"
	if err := enabled(ctx, wantEnabled); err != nil {
		return fmt.Errorf("restore manager service enablement: %w", err)
	}
	if err := d.clearOwnMaintenanceControl(ctx, intent, *principal); err != nil {
		return err
	}
	return nil
}

func (d *linuxInstallDriver) verifyUninstalled(ctx context.Context, intent linuxIntent, principal linuxPrincipal) error {
	status, err := d.inspectService(ctx)
	if err != nil || !linuxStatusStopped(status) {
		return errors.New("manager service is not stopped after uninstall")
	}
	for _, plan := range intent.Assets {
		state, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		actual := state.State()
		_ = state.Close()
		if plan.Role == linuxAssetUpdateTrust {
			if actual.Present && d.validateUpdateTrust(ctx) != nil {
				return errors.New("operator-controlled update trust is invalid after uninstall")
			}
			continue
		}
		if !matchesPlannedLinuxAsset(actual, plan.After) {
			return fmt.Errorf("deployment asset %s remains after uninstall", plan.Role)
		}
	}
	return nil
}

// recoverUpdaterBeforeNativeRollback handles a crash after this operation
// published its maintenance challenge but before it durably recorded ordinary
// updater reconciliation. A fresh updater snapshot is safe only while native
// asset replacement has not started because it validates image against receipt.
func (d *linuxInstallDriver) recoverUpdaterBeforeNativeRollback(ctx context.Context, intent linuxIntent, principal linuxPrincipal) error {
	evidence, _, _, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	if evidence.UpdaterReconciled {
		return nil
	}
	if len(evidence.Effects) != 0 {
		return errors.New("updater recovery is unsafe after native asset effects began")
	}
	inventory, inventoryPresent, inventoryErr := d.readInventory(ctx)
	if inventoryErr != nil {
		return inventoryErr
	}
	if intent.PriorInventory == nil {
		if inventoryPresent {
			return errors.New("Linux inventory appeared before updater recovery completed")
		}
	} else if !inventoryPresent || !reflect.DeepEqual(inventory, *intent.PriorInventory) {
		return errors.New("Linux inventory changed before updater recovery completed")
	}
	for _, plan := range intent.Assets {
		actual, inspectErr := inspectLinuxAsset(ctx, plan.Role)
		if inspectErr != nil {
			return inspectErr
		}
		state := actual.State()
		closeErr := actual.Close()
		if closeErr != nil {
			return closeErr
		}
		if plan.Role == linuxAssetUpdateTrust {
			if state.Present && d.validateUpdateTrust(ctx) != nil {
				return errors.New("operator-controlled update trust is invalid during updater recovery")
			}
			continue
		}
		if state != plan.Before {
			return fmt.Errorf("asset %s changed before updater recovery completed", plan.Role)
		}
	}
	// Remove only installer-owned maintenance files, leaving ordinary updater
	// state intact for the strict snapshot/reconcile bridge.
	if err := d.clearOwnMaintenanceControlPreservingUpdater(ctx, intent, principal); err != nil {
		return err
	}
	snapshot, err := d.session.SnapshotUpdater(ctx)
	if err != nil {
		return fmt.Errorf("capture updater state for interrupted reconciliation: %w", err)
	}
	managerBefore := linuxAssetState{}
	for _, plan := range intent.Assets {
		if plan.Role == linuxAssetManager {
			managerBefore = plan.Before
			break
		}
	}
	installed, present := snapshot.Installed()
	if present != managerBefore.Present || (present && installed.SHA256 != managerBefore.SHA256) {
		return errors.New("updater receipt differs from the untouched manager image before native rollback")
	}
	if err := d.session.ReconcileUpdater(ctx, snapshot); err != nil {
		return fmt.Errorf("reconcile updater state after interrupted maintenance: %w", err)
	}
	if err := d.markUpdaterReconciled(ctx); err != nil {
		return fmt.Errorf("persist recovered updater reconciliation proof: %w", err)
	}
	return nil
}

func (d *linuxInstallDriver) grantLaunchPermit(ctx context.Context, tx transaction, image Image) error {
	if d.hooks.grantLaunch != nil {
		return d.hooks.grantLaunch(ctx, tx, image)
	}
	return grantLinuxInstallerLaunch(ctx, tx, image)
}

func (d *linuxInstallDriver) revokeLaunchPermit(ctx context.Context, tx transaction) error {
	if d.hooks.revokeLaunch != nil {
		return d.hooks.revokeLaunch(ctx, tx)
	}
	return revokeLinuxInstallerLaunch(ctx, tx)
}

func (d *linuxInstallDriver) Rollback(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil {
		return errors.New("manager account evidence is unavailable for rollback")
	}
	if principal == nil {
		if intent.Prior != nil || intent.ServiceWasActive {
			return errors.New("manager account evidence is unavailable for rollback")
		}
		for _, plan := range intent.Assets {
			current, inspectErr := inspectLinuxAsset(ctx, plan.Role)
			if inspectErr != nil {
				return inspectErr
			}
			unchanged := current.State() == plan.Before
			closeErr := current.Close()
			if closeErr != nil || !unchanged {
				return errors.Join(errors.New("deployment asset changed before manager account provisioning"), closeErr)
			}
		}
		receipt, receiptErr := d.session.Receipt(ctx)
		if receiptErr != nil {
			return receiptErr
		}
		if _, present := receipt.Value(); present {
			return errors.New("installed manager receipt changed before account provisioning")
		}
		inventory, present, inventoryErr := d.readInventory(ctx)
		if inventoryErr != nil || present || inventory.OperationID != "" {
			return errors.Join(errors.New("Linux inventory changed before account provisioning"), inventoryErr)
		}
		return nil
	}
	status, err := d.inspectService(ctx)
	if err != nil {
		return err
	}
	if serviceActive(status) {
		if intent.Target == nil {
			return errors.New("unexpected manager service is active during rollback")
		}
		if err := stopLinuxManager(ctx); err != nil {
			return err
		}
	}
	if err := d.lockStoppedManager(ctx); err != nil {
		return err
	}
	// A candidate permit can survive any Verify failure, including a fresh
	// install with no prior service to restart. Revoke this operation's permit
	// while the manager is conclusively stopped and before rollback changes the
	// image or removes the journal. Unknown and foreign permits still fail
	// closed in the strict revoker.
	if err := d.revokeLaunchPermit(ctx, tx); err != nil {
		return err
	}
	if err := d.recoverUpdaterBeforeNativeRollback(ctx, intent, *principal); err != nil {
		return err
	}
	if tx.Mode == modeApply && intent.Prior == nil && intent.Target != nil {
		fresh, err := d.session.Receipt(ctx)
		if err != nil {
			return err
		}
		if err := d.session.RemoveInstalledForRollback(ctx, fresh, toInstalled(*intent.Target)); err != nil {
			return err
		}
	}
	if err := d.rollbackAssets(ctx, tx, intent); err != nil {
		return err
	}
	if tx.Mode == modeApply && intent.Target != nil {
		if intent.Prior != nil {
			if err := d.restoreReceipt(ctx, *intent.Prior); err != nil {
				return err
			}
		}
	}
	if err := d.reload(ctx); err != nil {
		return err
	}
	if intent.ServiceWasActive && intent.Prior != nil {
		rollback := rollbackIntent(intent)
		rollback.Nonce, err = d.rollbackNonce(ctx)
		if err != nil {
			return err
		}
		if err := d.clearOwnMaintenanceControl(ctx, intent, *principal); err != nil {
			return err
		}
		if err := d.writeMaintenanceChallenge(ctx, tx, rollback, *principal); err != nil {
			return err
		}
		if err := d.grantLaunchPermit(ctx, tx, *intent.Prior); err != nil {
			return err
		}
		if err := d.session.UnlockManager(); err != nil {
			d.dataHeld = false
			return err
		}
		d.dataHeld = false
		start := d.hooks.start
		if start == nil {
			start = startLinuxManager
		}
		if err := start(ctx); err != nil {
			return err
		}
		if err := d.waitMaintenanceReady(ctx, tx, rollback); err != nil {
			return err
		}
		if err := d.revokeLaunchPermit(ctx, tx); err != nil {
			return err
		}
	}
	if err := d.restoreManagerEnablementAfterRollback(ctx, tx, intent); err != nil {
		return err
	}
	if intent.PriorInventory != nil {
		if err := d.restorePriorInventory(ctx, tx, intent); err != nil {
			return err
		}
	} else if tx.Mode == modeApply {
		if err := d.publishRetainedFailureInventory(ctx, tx, intent, *principal); err != nil {
			return err
		}
	}
	return d.clearOwnMaintenanceControl(ctx, intent, *principal)
}

func (d *linuxInstallDriver) restoreManagerEnablementAfterRollback(ctx context.Context, tx transaction, intent linuxIntent) error {
	if tx.Mode == modeApply && intent.Prior == nil {
		// A fresh install captured no manager unit. Its rollback removes the
		// candidate unit before daemon-reload, so asking systemd to disable that
		// now-absent unit fails on supported distributions. Verify the exact
		// captured absence instead of weakening the general enable/disable API.
		status, err := d.inspectService(ctx)
		if err != nil || status.LoadState != "not-found" || !linuxStatusStopped(status) {
			return errors.Join(errors.New("fresh rollback did not restore the captured absent manager unit"), err)
		}
		return nil
	}
	enabled := d.hooks.enabled
	if enabled == nil {
		enabled = setLinuxManagerEnabled
	}
	return enabled(ctx, intent.ServiceWasEnabled)
}

func rollbackIntent(intent linuxIntent) linuxIntent {
	out := intent
	out.Target = intent.Prior
	out.Prior = intent.Target
	out.Mode = modeApply
	return out
}
func rollbackTransaction(tx transaction, prior Image) transaction {
	out := tx
	out.Mode = modeApply
	out.Target = &prior
	return out
}

func inventoryPointer(intent linuxIntent) *linuxInventory { return intent.PriorInventory }

func (d *linuxInstallDriver) restoreReceipt(ctx context.Context, prior Image) error {
	receipt, err := d.session.Receipt(ctx)
	if err != nil {
		return err
	}
	if current, ok := receipt.Value(); ok && current == toInstalled(prior) {
		return nil
	}
	return d.session.CompareAndSwapInstalled(ctx, receipt, toInstalled(prior))
}

func (d *linuxInstallDriver) restorePriorInventory(ctx context.Context, tx transaction, intent linuxIntent) error {
	if intent.PriorInventory == nil {
		return errors.New("captured prior Linux inventory is unavailable")
	}
	desired := *intent.PriorInventory
	if intent.Prior != nil {
		desired.Status = "installed"
		desired.Image = *intent.Prior
	}
	if len(desired.Assets) != len(intent.Assets) {
		return errors.New("captured prior asset inventory differs from immutable intent")
	}
	for i, plan := range intent.Assets {
		state, err := currentLinuxAsset(ctx, d.assets[plan.Role])
		if err != nil {
			return err
		}
		if plan.Role == linuxAssetUpdateTrust {
			if state.Present && d.validateUpdateTrust(ctx) != nil {
				return errors.New("operator-controlled update trust is invalid after rollback")
			}
			desired.Assets[i].State = state
			continue
		}
		priorState := rollbackPriorAssetState(intent, plan, i)
		if !sameLinuxAssetContent(state, priorState) {
			return fmt.Errorf("restored deployment asset %s differs from captured prior inventory", plan.Role)
		}
		desired.Assets[i].State = state
	}
	if err := validateLinuxInventory(desired); err != nil {
		return err
	}
	current, present, err := d.readInventory(ctx)
	if err != nil {
		return err
	}
	if present && reflect.DeepEqual(current, desired) {
		return nil
	}
	if !present {
		return errors.New("Linux inventory is not the exact operation image being rolled back")
	}
	priorCaptured := reflect.DeepEqual(current, *intent.PriorInventory)
	operationImage := Image{}
	operationStatus := "installed"
	if intent.Mode == modeApply {
		if intent.Target == nil {
			return errors.New("Linux rollback target image is unavailable")
		}
		operationImage = *intent.Target
	} else {
		operationStatus = "uninstalled"
		if intent.Prior == nil {
			return errors.New("uninstall rollback prior image is unavailable")
		}
		operationImage = *intent.Prior
	}
	operationCaptured := current.OperationID == tx.ID && current.Status == operationStatus && sameImage(current.Image, operationImage) && current.Principal == desired.Principal
	if operationCaptured {
		for i, plan := range intent.Assets {
			if !matchesPlannedLinuxAsset(current.Assets[i].State, plan.After) {
				operationCaptured = false
				break
			}
		}
	}
	if !priorCaptured && !operationCaptured {
		return errors.New("Linux inventory no longer matches captured prior or exact operation publication")
	}
	return d.publishInventory(ctx, desired, &current)
}

func rollbackPriorAssetState(intent linuxIntent, plan linuxAssetPlan, index int) linuxAssetState {
	if plan.Role == linuxAssetManager && intent.Prior != nil {
		// A terminal updater may have replaced only the manager executable after
		// the inventory was written. The typed prior receipt and captured preimage
		// are the authorized image/bytes to restore in that case.
		return plan.Before
	}
	return intent.PriorInventory.Assets[index].State
}

func (d *linuxInstallDriver) publishRetainedFailureInventory(ctx context.Context, tx transaction, intent linuxIntent, principal linuxPrincipal) error {
	if intent.Prior != nil || intent.PriorInventory != nil || intent.Target == nil || intent.ServiceWasActive || intent.ServiceWasEnabled {
		return errors.New("retained failed-install inventory has an unexpected prior state")
	}
	account, present, err := d.inspectAccount(ctx)
	if err != nil || !present || !sameLinuxPrincipal(account, principal) {
		return errors.New("retained manager account evidence differs from host")
	}
	if err := ensureLinuxDataDirectories(ctx, d.hooks.dataRoot, principal, false); err != nil {
		return err
	}
	service, err := d.inspectService(ctx)
	if err != nil || serviceActive(service) || service.UnitFileState == "enabled" {
		return errors.New("retained failed installation manager service is active or enabled")
	}
	receipt, err := d.session.Receipt(ctx)
	if err != nil {
		return err
	}
	if _, present := receipt.Value(); present {
		return errors.New("retained failed installation unexpectedly has an installed receipt")
	}
	assets := make([]linuxOwnedAsset, 0, len(intent.Assets))
	for _, plan := range intent.Assets {
		snapshot, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		state := snapshot.State()
		_ = snapshot.Close()
		if isLinuxDeploymentAsset(plan.Role) && state.Present {
			return fmt.Errorf("retained failed installation still owns deployment asset %s", plan.Role)
		}
		if plan.Role == linuxAssetBootstrapTrust && state.Present && !sameLinuxAssetContent(state, plan.After) {
			return errors.New("retained bootstrap trust differs from authenticated trust input")
		}
		if plan.Role == linuxAssetUpdateTrust && state.Present {
			return errors.New("retained failed installation has an unproved update-trust asset")
		}
		assets = append(assets, linuxOwnedAsset{Role: plan.Role, State: state})
	}
	retained := linuxInventory{Format: 1, Status: "retained", OperationID: tx.ID, Image: *intent.Target, Principal: principal, Assets: assets}
	if intent.PriorInventory != nil && intent.PriorInventory.Status == "retained" {
		retained = *intent.PriorInventory
	}
	if err := validateLinuxInventory(retained); err != nil {
		return err
	}
	current, present, err := d.readInventory(ctx)
	if err != nil {
		return err
	}
	if present {
		if current.Status == "retained" && reflect.DeepEqual(current, retained) {
			return nil
		}
		if current.Status != "installed" || current.OperationID != tx.ID || !sameImage(current.Image, *intent.Target) {
			return errors.New("failed-install inventory cannot replace an unrelated protected record")
		}
		return d.publishInventory(ctx, retained, &current)
	}
	return d.publishInventory(ctx, retained, nil)
}

func isLinuxDeploymentAsset(role linuxAssetRole) bool {
	return role != linuxAssetBootstrapTrust && role != linuxAssetUpdateTrust
}

func (d *linuxInstallDriver) Finish(ctx context.Context, tx transaction) error {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	result := terminalResult(tx)
	if tx.Phase == phaseCommitted {
		result = committedResult(tx)
	}
	body, err := json.Marshal(struct {
		Outcome     Outcome `json:"outcome"`
		OperationID string  `json:"operation_id"`
		Image       *Image  `json:"image,omitempty"`
		FailedStage string  `json:"failed_stage"`
	}{result.Outcome, result.OperationID, result.Image, result.FailedStage})
	if err != nil || len(body) > 4096 {
		return errors.New("installer terminal result is invalid")
	}
	if err := d.writeTerminalResult(ctx, body); err != nil {
		return err
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err == nil && principal != nil {
		if err := d.clearOwnMaintenanceControl(ctx, intent, *principal); err != nil {
			return err
		}
	}
	if err := d.removeExactTerminalJournal(ctx, tx); err != nil {
		return err
	}
	return nil
}

func (d *linuxInstallDriver) writeTerminalResult(ctx context.Context, body []byte) error {
	info, err := d.operation.Lstat(linuxResultName)
	if errors.Is(err, os.ErrNotExist) {
		return writeLinuxProtectedState(ctx, d.operation, linuxResultName, body, nil, d.hooks.testRoot)
	}
	if err != nil {
		return err
	}
	if err := checkProtectedFile(info, 0); err != nil || info.Mode().Perm() != 0600 || info.Size() != int64(len(body)) {
		return errors.New("existing terminal result is unsafe")
	}
	file, err := d.operation.Open(linuxResultName)
	if err != nil {
		return err
	}
	opened, statErr := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	after, statAfterErr := d.operation.Lstat(linuxResultName)
	if statErr != nil || readErr != nil || closeErr != nil || statAfterErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, after) || !bytes.Equal(raw, body) {
		return errors.Join(errors.New("existing terminal result differs from completed operation"), statErr, readErr, closeErr, statAfterErr)
	}
	return nil
}

func (d *linuxInstallDriver) removeExactTerminalJournal(ctx context.Context, tx transaction) error {
	if tx.Phase != phaseCommitted && tx.Phase != phaseRolledBack {
		return errors.New("installer journal is not terminal")
	}
	if d.store.snapshot == nil || !d.store.snapshot.present || !sameImmutableTransaction(d.store.snapshot.tx, tx) {
		return errors.New("loaded installer journal identity is unavailable")
	}
	currentRaw, currentInfo, present, err := d.store.readJournal(ctx)
	if err != nil || !present || !os.SameFile(currentInfo, d.store.snapshot.info) || !bytesEqual(currentRaw, d.store.snapshot.raw) {
		return errors.Join(errors.New("terminal installer journal changed before retirement"), err)
	}
	if err := d.state.Remove(journalName); err != nil {
		return err
	}
	if err := syncDirectory(d.state); err != nil {
		return err
	}
	d.store.snapshot = &journalSnapshot{}
	return nil
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

func (d *linuxInstallDriver) rollbackAssets(ctx context.Context, tx transaction, intent linuxIntent) error {
	evidence, _, _, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	for i := len(intent.Assets) - 1; i >= 0; i-- {
		plan := intent.Assets[i]
		before, staged, found := latestLinuxEffect(evidence.Effects, plan.Role)
		if found {
			if _, err := reconcileLinuxAssetEffect(ctx, tx.ID, plan.Role, before, staged); err != nil {
				return err
			}
		}
		current, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		state := current.State()
		_ = current.Close()
		if plan.Role == linuxAssetUpdateTrust {
			if state.Present && d.validateUpdateTrust(ctx) != nil {
				return errors.New("operator-controlled update trust changed to an invalid value during rollback")
			}
			if err := d.rebindRollbackAssetSnapshot(ctx, plan.Role, state); err != nil {
				return err
			}
			continue
		}
		if !found && state != plan.Before {
			return fmt.Errorf("deployment asset %s changed without recorded installer ownership", plan.Role)
		}
		if state == plan.Before {
			if err := d.rebindRollbackAssetSnapshot(ctx, plan.Role, state); err != nil {
				return err
			}
			continue
		}
		ownedIdentity := false
		for j := len(evidence.Effects) - 1; j >= 0; j-- {
			effect := evidence.Effects[j]
			if effect.Role == plan.Role && (effect.Staged == state || effect.Actual == state) {
				ownedIdentity = true
				break
			}
		}
		if !ownedIdentity {
			return fmt.Errorf("unknown replacement for deployment asset %s requires manual repair", plan.Role)
		}
		if intent.PriorInventory == nil && plan.Role == linuxAssetBootstrapTrust && matchesPlannedLinuxAsset(state, plan.After) {
			// The first independent trust key remains as provenance for a retained
			// failed-install record. It is kept only when it is the exact staged
			// trust asset from this authenticated intent.
			continue
		}
		if !matchesPlannedLinuxAsset(state, plan.After) {
			return fmt.Errorf("deployment asset %s is not the recorded candidate image", plan.Role)
		}
		snapshot, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		if snapshot.State() != state {
			_ = snapshot.Close()
			return errors.New("deployment asset changed during rollback")
		}
		if plan.Before.Present {
			file, err := d.openPreimage(string(plan.Role))
			if err != nil {
				_ = snapshot.Close()
				return err
			}
			restorePlan := plan.Before
			restorePlan.Device, restorePlan.Inode = 0, 0
			actual, publishErr := publishLinuxAssetRecorded(ctx, tx.ID, snapshot, file, restorePlan, func(staged linuxAssetState) error { return d.recordEffect(ctx, plan.Role, "publish", state, staged) })
			closeErr := file.Close()
			if publishErr != nil || closeErr != nil {
				_ = snapshot.Close()
				return errors.Join(publishErr, closeErr)
			}
			if err := d.completeEffect(ctx, plan.Role, "publish", actual); err != nil {
				_ = snapshot.Close()
				return err
			}
		} else {
			if err := d.recordEffect(ctx, plan.Role, "remove", state, linuxAssetState{}); err != nil {
				_ = snapshot.Close()
				return err
			}
			if err := removeLinuxAsset(ctx, snapshot); err != nil {
				_ = snapshot.Close()
				return err
			}
			if err := d.completeEffect(ctx, plan.Role, "remove", linuxAssetState{}); err != nil {
				_ = snapshot.Close()
				return err
			}
		}
		_ = snapshot.Close()
		check, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return err
		}
		if !sameLinuxAssetContent(check.State(), plan.Before) {
			_ = check.Close()
			return errors.New("deployment asset preimage did not restore")
		}
		_ = d.replaceAssetSnapshot(plan.Role, check)
	}
	return nil
}

func (d *linuxInstallDriver) rebindRollbackAssetSnapshot(ctx context.Context, role linuxAssetRole, expected linuxAssetState) error {
	snapshot, err := inspectLinuxAsset(ctx, role)
	if err != nil {
		return err
	}
	current, err := currentLinuxAsset(ctx, snapshot)
	if err != nil || snapshot.State() != expected || current != expected {
		_ = snapshot.Close()
		return errors.Join(fmt.Errorf("deployment asset %s changed while binding rollback snapshot", role), err)
	}
	return d.replaceAssetSnapshot(role, snapshot)
}

func latestLinuxEffect(effects []linuxNativeEffect, role linuxAssetRole) (linuxAssetState, linuxAssetState, bool) {
	for i := len(effects) - 1; i >= 0; i-- {
		if effects[i].Role == role {
			return effects[i].Before, effects[i].Staged, true
		}
	}
	return linuxAssetState{}, linuxAssetState{}, false
}

func checkReadOnlyLinuxLayout(ctx context.Context, state *os.Root, inventory *linuxInventory) (CheckReport, error) {
	if ctx == nil || state == nil {
		return CheckReport{}, errors.New("Linux installer check context is unavailable")
	}
	if _, err := state.Lstat(pendingName); err == nil {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return CheckReport{}, err
	}
	if _, err := state.Lstat(journalName); err == nil {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return CheckReport{}, err
	}
	if inventory == nil {
		orphaned, err := linuxNativeArtifactsWithoutInventory(ctx, linuxInstallerData)
		if err != nil {
			return CheckReport{}, err
		}
		if orphaned {
			return CheckReport{State: "drifted"}, nil
		}
		return CheckReport{State: "absent"}, nil
	}
	if err := validateLinuxInventory(*inventory); err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	return CheckReport{State: inventory.Status, Coherent: true, Image: imagePointer(inventory.Image)}, nil
}

func checkLinuxInstallation(ctx context.Context) (CheckReport, error) {
	if ctx == nil {
		return CheckReport{}, errors.New("Linux installer check context is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return CheckReport{}, err
	}
	// Read-only checks intentionally avoid AcquireLinuxInstaller, which creates
	// the updater state root and lock file during first installation.
	root, err := safepath.OpenRoot(ctx, linuxInstallerState)
	if errors.Is(err, os.ErrNotExist) {
		orphaned, inspectErr := linuxNativeArtifactsWithoutInventory(ctx, linuxInstallerData)
		if inspectErr != nil {
			return CheckReport{State: "drifted"}, nil
		}
		if orphaned {
			return CheckReport{State: "drifted"}, nil
		}
		return CheckReport{State: "absent"}, nil
	}
	if err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	defer root.Close()
	store, err := newJournalStore(ctx, root)
	if err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	defer store.Close()
	tx, err := store.Load(ctx)
	if err != nil {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	}
	if tx != nil {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	}
	updater, err := readLinuxCheckReceiptAndPending(ctx, root)
	if updater.Pending {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	}
	if err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	receipt, receiptPresent := updater.Installed, updater.Present
	orphaned, err := checkLinuxOperationDirectories(ctx, root, linuxInstallerState)
	if err != nil {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	}
	if orphaned {
		return CheckReport{State: "recovery_required", Pending: true}, nil
	}
	d := &linuxInstallDriver{state: root, store: store, assets: make(map[linuxAssetRole]*linuxAssetSnapshot), hooks: linuxNativeHooks{stateRoot: linuxInstallerState, dataRoot: linuxInstallerData}}
	inventory, present, err := d.readInventory(ctx)
	if err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	if !present {
		if receiptPresent {
			return CheckReport{State: "drifted"}, nil
		}
		orphaned, inspectErr := linuxNativeArtifactsWithoutInventory(ctx, linuxInstallerData)
		if inspectErr != nil {
			return CheckReport{State: "drifted"}, nil
		}
		if orphaned {
			return CheckReport{State: "drifted"}, nil
		}
		return CheckReport{State: "absent"}, nil
	}
	if err := d.inspectAllAssets(ctx); err != nil {
		return CheckReport{State: "drifted"}, nil
	}
	defer func() {
		for _, snap := range d.assets {
			_ = snap.Close()
		}
	}()
	assetsMatch := d.assetsMatchInventory(inventory)
	superseded := !assetsMatch && updater.Terminal && receiptPresent && d.assetsMatchTerminalUpdaterSupersession(inventory, receipt)
	if !assetsMatch && !superseded {
		return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
	}
	if updateTrust := d.assets[linuxAssetUpdateTrust]; updateTrust != nil && updateTrust.State().Present && d.validateUpdateTrust(ctx) != nil {
		return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
	}
	if inventory.Status == "retained" {
		if receiptPresent {
			return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
		}
		service, err := inspectLinuxManager(ctx)
		if err != nil || linuxStatusActive(service) || service.UnitFileState == "enabled" {
			return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
		}
		account, accountPresent, err := lookupLinuxInstallerAccount(ctx)
		if err != nil || !accountPresent || !sameLinuxPrincipal(account, inventory.Principal) {
			return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
		}
		check := &linuxInstallDriver{state: root, store: store, hooks: linuxNativeHooks{stateRoot: linuxInstallerState, dataRoot: linuxInstallerData}}
		if _, err := check.retainedInventoryOrigin(ctx, inventory); err != nil {
			return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
		}
		if err := verifyLinuxDataDirectoriesReadOnly(ctx, linuxInstallerData, inventory.Principal); err != nil {
			return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
		}
		return CheckReport{State: "retained", Coherent: false, Image: imagePointer(inventory.Image)}, nil
	}
	currentImage := inventory.Image
	if superseded {
		currentImage = fromInstalled(receipt)
	}
	if !receiptPresent || !sameImage(currentImage, fromInstalled(receipt)) {
		return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
	}
	service, err := inspectLinuxManager(ctx)
	if err != nil {
		return CheckReport{State: "drifted", Image: imagePointer(inventory.Image)}, nil
	}
	if inventory.Status == "installed" {
		if !linuxStatusActive(service) {
			return CheckReport{State: "drifted", Image: imagePointer(currentImage)}, nil
		}
		if err := requireLinuxManagerImage(ctx, currentImage); err != nil {
			return CheckReport{State: "drifted", Image: imagePointer(currentImage)}, nil
		}
	} else if !linuxStatusStopped(service) {
		return CheckReport{State: "drifted", Image: imagePointer(currentImage)}, nil
	}
	return CheckReport{State: inventory.Status, Coherent: true, Image: imagePointer(currentImage)}, nil
}

func readLinuxCheckReceiptAndPending(ctx context.Context, supplied *os.Root) (selfupdate.LinuxInstallerInspection, error) {
	if supplied == nil || ctx == nil {
		return selfupdate.LinuxInstallerInspection{}, errors.New("protected updater state is unavailable")
	}
	canonical, err := safepath.OpenRoot(ctx, linuxInstallerState)
	if err != nil {
		return selfupdate.LinuxInstallerInspection{}, errors.New("canonical updater state could not be inspected")
	}
	defer canonical.Close()
	initial, err := supplied.Stat(".")
	canonicalInfo, canonicalErr := canonical.Stat(".")
	if err != nil || canonicalErr != nil || !os.SameFile(initial, canonicalInfo) || validateDirectory(initial, 0, true) != nil {
		return selfupdate.LinuxInstallerInspection{}, errors.New("installer check root is not the canonical protected updater state")
	}
	inspection, inspectErr := selfupdate.InspectLinuxInstallerState(ctx)
	after, afterErr := supplied.Stat(".")
	canonicalAfter, canonicalAfterErr := canonical.Stat(".")
	if afterErr != nil || canonicalAfterErr != nil || !os.SameFile(initial, after) || !os.SameFile(initial, canonicalAfter) {
		return selfupdate.LinuxInstallerInspection{}, errors.New("protected updater state changed during read-only inspection")
	}
	if inspectErr != nil {
		return inspection, inspectErr
	}
	return inspection, nil
}

func checkLinuxOperationDirectories(ctx context.Context, state *os.Root, statePath string) (bool, error) {
	info, err := state.Lstat(installDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || validateDirectory(info, 0, true) != nil {
		return false, errors.Join(errors.New("installer operation directory is unsafe"), err)
	}
	install, err := state.OpenRoot(installDirectory)
	if err != nil {
		return false, err
	}
	defer install.Close()
	entries, err := readLinuxRootDir(install)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		id := entry.Name()
		if !lowerHex(id, 64) || !entry.IsDir() {
			return false, errors.New("unrecognized installer operation residue")
		}
		op, err := install.OpenRoot(id)
		if err != nil {
			return false, err
		}
		if err := checkPrivateOperationDir(op); err != nil {
			_ = op.Close()
			return false, err
		}
		if err := validateTerminalOperationContents(ctx, op, false); err != nil {
			_ = op.Close()
			return false, fmt.Errorf("installer operation contains pending or unknown state: %w", err)
		}
		if _, err := op.Lstat(linuxResultName); errors.Is(err, os.ErrNotExist) {
			_ = op.Close()
			return true, nil
		} else if err != nil {
			_ = op.Close()
			return false, err
		}
		if _, present, err := readLinuxInstallerResultAt(ctx, statePath, id, 0); err != nil || !present {
			_ = op.Close()
			return false, errors.Join(errors.New("retained installer terminal result is invalid"), err)
		}
		if err := op.Close(); err != nil {
			return false, err
		}
	}
	return false, nil
}

// linuxNativeArtifactsWithoutInventory is intentionally read-only. It detects
// fixed-path residue that would make a later fresh install ambiguous without
// creating state directories, looking inside manager.db, or touching services.
func linuxNativeArtifactsWithoutInventory(ctx context.Context, dataRoot string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if _, err := user.Lookup(linuxInstallerAccount); err == nil {
		return true, nil
	} else if !errors.Is(err, user.UnknownUserError(linuxInstallerAccount)) {
		return false, err
	}
	if info, err := os.Lstat(dataRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return false, errors.New("fixed manager data path is unsafe")
		}
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	service, err := inspectLinuxManager(ctx)
	if err != nil {
		return false, err
	}
	if service.LoadState == "loaded" || serviceActive(service) {
		return true, nil
	}
	for _, spec := range fixedLinuxAssets {
		if spec.Role == linuxAssetUpdateTrust {
			continue // optional operator-controlled updater policy is not an install artifact
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		snapshot, err := inspectLinuxAsset(ctx, spec.Role)
		if err != nil {
			return false, err
		}
		present := snapshot.State().Present
		closeErr := snapshot.Close()
		if closeErr != nil {
			return false, closeErr
		}
		if present {
			return true, nil
		}
	}
	return false, nil
}
