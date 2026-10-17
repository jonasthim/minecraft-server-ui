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
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

// RecoverPending is called only by explicit local recovery while the updater
// lock is held. Ordinary Load, check, and apply remain read-only on pending
// publications.
func (d *linuxInstallDriver) RecoverPending(ctx context.Context) error {
	if d == nil || d.store == nil || d.operation != nil || d.intent != nil {
		return errors.New("Linux pending recovery is unavailable")
	}
	if err := d.store.RecoverPending(ctx); err != nil {
		return err
	}
	tx, err := d.Load(ctx)
	if err != nil {
		return err
	}
	if tx == nil {
		if _, _, err := readProtectedPending(ctx, d.state, "installation.json.pending", maxLinuxInventoryBytes, d.hooks.testRoot); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(errors.New("orphaned Linux inventory publication requires review"), err)
		}
		orphaned, err := checkLinuxOperationDirectories(ctx, d.state, d.hooks.stateRoot)
		if err != nil || orphaned {
			return errors.Join(errors.New("orphaned installer operation state requires review"), err)
		}
		return nil
	}
	if err := d.recoverEffectsPending(ctx); err != nil {
		return err
	}
	if err := d.recoverInventoryPending(ctx, *tx); err != nil {
		return err
	}
	if err := d.recoverResultPending(ctx, *tx); err != nil {
		return err
	}
	return nil
}

func readProtectedPending(ctx context.Context, root *os.Root, name string, max int64, testRoot bool) ([]byte, os.FileInfo, error) {
	var zero os.FileInfo
	if ctx == nil || root == nil || ctx.Err() != nil {
		return nil, zero, errors.New("pending publication root is unavailable")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, zero, err
	}
	if !linuxNativeProtectedFile(info, testRoot) || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > max {
		return nil, zero, errors.New("pending publication file is unsafe")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, zero, err
	}
	opened, statErr := f.Stat()
	body, readErr := io.ReadAll(io.LimitReader(f, max+1))
	if readErr == nil {
		readErr = f.Sync()
	}
	closeErr := f.Close()
	after, afterErr := root.Lstat(name)
	if statErr != nil || readErr != nil || closeErr != nil || afterErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, after) || len(body) != int(info.Size()) {
		return nil, zero, errors.Join(errors.New("pending publication changed while reading"), statErr, readErr, closeErr, afterErr)
	}
	return body, info, nil
}

func validateTerminalOperationContents(ctx context.Context, operation *os.Root, testRoot bool) error {
	entries, err := readLinuxRootDir(operation)
	if err != nil {
		return err
	}
	if len(entries) > 8 {
		return errors.New("installer operation directory exceeds its entry bound")
	}
	allowed := map[string]bool{
		intentFileName: true, linuxExecutorName: true, linuxEffectsName: true,
		linuxResultName: true, "inputs": true, "preimage": true, "source-bootstrap-trust": true,
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !allowed[name] || seen[name] {
			return fmt.Errorf("unknown installer operation entry %q", name)
		}
		seen[name] = true
		info, err := operation.Lstat(name)
		if err != nil {
			return err
		}
		if name == "inputs" || name == "preimage" {
			uid := uint32(0)
			if testRoot {
				uid = uint32(os.Geteuid())
			}
			if validateDirectory(info, uid, true) != nil || info.Mode().Perm() != 0700 {
				return errors.New("installer operation subdirectory is unsafe")
			}
			continue
		}
		wantMode := uint32(0600)
		if name == linuxExecutorName {
			wantMode = 0755
		}
		max := int64(maxIntentBytes)
		if name == linuxExecutorName {
			max = candidate.MaxBinaryBytes
		} else if name == linuxEffectsName {
			max = linuxMaxEffectsBytes
		} else if name == linuxResultName {
			max = 4096
		} else if name == "source-bootstrap-trust" {
			max = maxLinuxBootstrapTrustBytes
		}
		if !linuxNativeProtectedFile(info, testRoot) || uint32(info.Mode().Perm()) != wantMode || info.Size() <= 0 || info.Size() > max {
			return fmt.Errorf("installer operation file %q is unsafe", name)
		}
	}
	for _, required := range []string{intentFileName, linuxEffectsName, linuxResultName} {
		if !seen[required] {
			return fmt.Errorf("terminal installer operation is missing %q", required)
		}
	}
	return nil
}

func promoteProtectedPending(ctx context.Context, root *os.Root, pending, destination string, body []byte, pendingInfo, priorInfo os.FileInfo, priorBody []byte, testRoot bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := root.Lstat(destination); priorInfo == nil {
		if !errors.Is(err, os.ErrNotExist) {
			return errors.Join(errors.New("pending publication has an unexpected destination"), err)
		}
	} else if err != nil {
		return err
	} else {
		current, _ := root.Lstat(destination)
		if !os.SameFile(priorInfo, current) {
			return errors.New("pending publication destination identity changed")
		}
		currentBody, currentReadInfo, readErr := readProtectedPending(ctx, root, destination, int64(len(priorBody)), testRoot)
		if readErr != nil || !os.SameFile(priorInfo, currentReadInfo) || !bytes.Equal(currentBody, priorBody) {
			return errors.Join(errors.New("pending publication destination bytes changed"), readErr)
		}
	}
	latest, err := root.Lstat(pending)
	if err != nil || !os.SameFile(pendingInfo, latest) {
		return errors.Join(errors.New("pending publication identity changed"), err)
	}
	readback, readInfo, err := readProtectedPending(ctx, root, pending, int64(len(body)), testRoot)
	if err != nil || !os.SameFile(pendingInfo, readInfo) || !bytes.Equal(readback, body) {
		return errors.Join(errors.New("pending publication bytes changed before promotion"), err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Rename(pending, destination); err != nil {
		return fmt.Errorf("promote complete pending publication: %w", err)
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	final, err := root.Lstat(destination)
	if err != nil || !os.SameFile(pendingInfo, final) {
		return errors.Join(errors.New("pending publication result is uncertain"), err)
	}
	return nil
}

func (d *linuxInstallDriver) recoverEffectsPending(ctx context.Context) error {
	body, pendingInfo, err := readProtectedPending(ctx, d.operation, "effects.pending", linuxMaxEffectsBytes, d.hooks.testRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var next linuxNativeEvidence
	if err := decodeLinuxStrictJSON(body, linuxMaxEffectsBytes, &next); err != nil || next.Format != 1 || next.OperationID != d.intent.ID || len(next.Effects) > 128 {
		return errors.New("pending native effects evidence is malformed")
	}
	canonical, err := json.Marshal(next)
	if err != nil || !bytes.Equal(canonical, body) {
		return errors.New("pending native effects evidence is not canonical")
	}
	current, currentBody, currentInfo, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	if !validNativeEvidenceTransition(current, next) {
		return errors.New("pending native effects evidence is not a valid next publication")
	}
	if !reflect.DeepEqual(current, next) {
		if err := d.validatePendingEffectState(ctx, current, next); err != nil {
			return err
		}
	}
	return promoteProtectedPending(ctx, d.operation, "effects.pending", linuxEffectsName, body, pendingInfo, currentInfo, currentBody, d.hooks.testRoot)
}

func validNativeEvidenceTransition(current, next linuxNativeEvidence) bool {
	if current.Format != next.Format || current.OperationID != next.OperationID || current.AccountCreated && !next.AccountCreated || current.UpdaterReconciled && !next.UpdaterReconciled {
		return false
	}
	if current.Principal != nil && (next.Principal == nil || *current.Principal != *next.Principal) {
		return false
	}
	if next.Principal != nil && !validLinuxPrincipal(*next.Principal) || next.AccountCreated && next.Principal == nil {
		return false
	}
	if current.RollbackNonce != "" && current.RollbackNonce != next.RollbackNonce || next.RollbackNonce != "" && !lowerHex(next.RollbackNonce, 64) {
		return false
	}
	if len(next.Effects) < len(current.Effects) || len(next.Effects) > len(current.Effects)+1 {
		return false
	}
	for i := range current.Effects {
		if i >= len(next.Effects) {
			return false
		}
		old, new := current.Effects[i], next.Effects[i]
		if old == new {
			continue
		}
		if i != len(current.Effects)-1 || old.Complete || !new.Complete || old.Role != new.Role || old.Direction != new.Direction || old.Before != new.Before || old.Staged != new.Staged || !validLinuxCapturedState(old.Role, new.Actual) {
			return false
		}
	}
	if len(next.Effects) == len(current.Effects)+1 {
		added := next.Effects[len(current.Effects)]
		if len(current.Effects) > 0 && !current.Effects[len(current.Effects)-1].Complete || added.Complete || added.Actual != (linuxAssetState{}) {
			return false
		}
	}
	changes := 0
	if !reflect.DeepEqual(current.Principal, next.Principal) || current.AccountCreated != next.AccountCreated {
		changes++
	}
	if current.RollbackNonce != next.RollbackNonce {
		changes++
	}
	if current.UpdaterReconciled != next.UpdaterReconciled {
		changes++
	}
	if !reflect.DeepEqual(current.Effects, next.Effects) {
		changes++
	}
	return changes == 1
}

func (d *linuxInstallDriver) validatePendingEffectState(ctx context.Context, current, next linuxNativeEvidence) error {
	if len(next.Effects) == len(current.Effects)+1 {
		effect := next.Effects[len(next.Effects)-1]
		actual, err := inspectLinuxAsset(ctx, effect.Role)
		if err != nil {
			return err
		}
		state := actual.State()
		closeErr := actual.Close()
		if closeErr != nil || state != effect.Before {
			return errors.Join(errors.New("pending native effect was not published before its evidence"), closeErr)
		}
		return nil
	}
	for i := range current.Effects {
		if current.Effects[i] == next.Effects[i] {
			continue
		}
		effect := next.Effects[i]
		actual, err := inspectLinuxAsset(ctx, effect.Role)
		if err != nil {
			return err
		}
		state := actual.State()
		closeErr := actual.Close()
		if closeErr != nil || state != effect.Actual {
			return errors.Join(errors.New("pending native effect completion differs from the actual asset"), closeErr)
		}
		return nil
	}
	if current.UpdaterReconciled != next.UpdaterReconciled && len(current.Effects) != 0 {
		return errors.New("pending updater marker follows native effects")
	}
	return nil
}

func (d *linuxInstallDriver) recoverInventoryPending(ctx context.Context, tx transaction) error {
	body, pendingInfo, err := readProtectedPending(ctx, d.state, "installation.json.pending", maxLinuxInventoryBytes, d.hooks.testRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pending, err := decodeLinuxInventory(body)
	if err != nil {
		return errors.New("pending Linux inventory is malformed")
	}
	current, present, err := d.readInventory(ctx)
	if err != nil {
		return err
	}
	priorBody, priorInfo, err := readProtectedPending(ctx, d.state, "installation.json", maxLinuxInventoryBytes, d.hooks.testRoot)
	if errors.Is(err, os.ErrNotExist) {
		priorBody = nil
		priorInfo = nil
	} else if err != nil {
		return err
	}
	if !present && priorInfo != nil || present && priorInfo == nil {
		return errors.New("Linux inventory preimage changed during pending recovery")
	}
	if tx.Phase == phaseReplacing {
		if d.intent.PriorInventory == nil && present || d.intent.PriorInventory != nil && (!present || !reflect.DeepEqual(current, *d.intent.PriorInventory)) {
			return errors.New("pending Linux inventory does not follow its captured preimage")
		}
	} else if tx.Phase == phaseRollingBack {
		if d.intent.PriorInventory != nil {
			if !present {
				return errors.New("rollback inventory preimage is missing")
			}
			priorCaptured := reflect.DeepEqual(current, *d.intent.PriorInventory)
			operationImage, operationStatus := Image{}, "installed"
			if tx.Mode == modeApply {
				if d.intent.Target == nil {
					return errors.New("rollback target image is unavailable")
				}
				operationImage = *d.intent.Target
			} else {
				operationStatus = "uninstalled"
				if d.intent.Prior == nil {
					return errors.New("uninstall rollback image is unavailable")
				}
				operationImage = *d.intent.Prior
			}
			operationCaptured := current.OperationID == tx.ID && current.Status == operationStatus && sameImage(current.Image, operationImage) && current.Principal == d.intent.PriorInventory.Principal
			if operationCaptured {
				for i, plan := range d.intent.Assets {
					if !matchesPlannedLinuxAsset(current.Assets[i].State, plan.After) {
						operationCaptured = false
						break
					}
				}
			}
			if !priorCaptured && !operationCaptured {
				return errors.New("rollback inventory preimage is neither captured prior nor exact operation publication")
			}
		} else if present {
			if tx.Mode != modeApply || d.intent.Target == nil || current.OperationID != tx.ID || current.Status != "installed" || !sameImage(current.Image, *d.intent.Target) {
				return errors.New("first-install rollback inventory preimage is not the exact candidate publication")
			}
			for i, plan := range d.intent.Assets {
				if !matchesPlannedLinuxAsset(current.Assets[i].State, plan.After) {
					return errors.New("first-install rollback inventory preimage differs from candidate assets")
				}
			}
		}
	} else {
		return errors.New("pending Linux inventory is outside a recoverable phase")
	}
	if tx.Phase == phaseRollingBack {
		if err := d.validatePendingTargetInventory(ctx, tx, pending); err == nil {
			assetsStillPublished, rollbackStarted, stateErr := d.classifyPendingTargetAssets(ctx, pending)
			if stateErr != nil {
				return stateErr
			}
			if assetsStillPublished {
				return promoteProtectedPending(ctx, d.state, "installation.json.pending", "installation.json", body, pendingInfo, priorInfo, priorBody, d.hooks.testRoot)
			}
			if rollbackStarted {
				return removeProtectedPending(ctx, d.state, "installation.json.pending", body, pendingInfo, priorInfo, priorBody, d.hooks.testRoot)
			}
		}
	}
	if err := d.validatePendingInventory(ctx, tx, pending); err != nil {
		return err
	}
	return promoteProtectedPending(ctx, d.state, "installation.json.pending", "installation.json", body, pendingInfo, priorInfo, priorBody, d.hooks.testRoot)
}

func removeProtectedPending(ctx context.Context, root *os.Root, name string, body []byte, info, priorInfo os.FileInfo, priorBody []byte, testRoot bool) error {
	current, rawInfo, err := readProtectedPending(ctx, root, name, int64(len(body)), testRoot)
	if err != nil || !os.SameFile(info, rawInfo) || !bytes.Equal(current, body) {
		return errors.Join(errors.New("pending publication changed before exact removal"), err)
	}
	if priorInfo == nil {
		if _, err := root.Lstat("installation.json"); !errors.Is(err, os.ErrNotExist) {
			return errors.Join(errors.New("inventory destination appeared before pending removal"), err)
		}
	} else {
		currentInfo, err := root.Lstat("installation.json")
		if err != nil || !os.SameFile(priorInfo, currentInfo) {
			return errors.Join(errors.New("inventory destination changed before pending removal"), err)
		}
		currentBody, readInfo, readErr := readProtectedPending(ctx, root, "installation.json", int64(len(priorBody)), testRoot)
		if readErr != nil || !os.SameFile(priorInfo, readInfo) || !bytes.Equal(currentBody, priorBody) {
			return errors.Join(errors.New("inventory destination bytes changed before pending removal"), readErr)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncDirectory(root)
}

func (d *linuxInstallDriver) validatePendingTargetInventory(ctx context.Context, tx transaction, pending linuxInventory) error {
	image, status := d.intent.Target, "installed"
	if tx.Mode == modeUninstall {
		image, status = d.intent.Prior, "uninstalled"
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil || image == nil || principal == nil || pending.Status != status || pending.OperationID != tx.ID || !sameImage(pending.Image, *image) || pending.Principal != *principal || len(pending.Assets) != len(d.intent.Assets) {
		return errors.New("pending target inventory is not bound to operation intent")
	}
	evidence, _, _, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	for i, plan := range d.intent.Assets {
		state := pending.Assets[i].State
		if pending.Assets[i].Role != plan.Role || !sameLinuxAssetContent(state, plan.After) {
			return errors.New("pending target inventory differs from immutable asset plan")
		}
		if state == plan.Before {
			continue
		}
		owned := false
		for _, effect := range evidence.Effects {
			if effect.Role == plan.Role && effect.Direction == "publish" && effect.Before == plan.Before && (effect.Staged == state || effect.Actual == state) {
				owned = true
				break
			}
			if !plan.After.Present && effect.Role == plan.Role && effect.Direction == "remove" && effect.Before == plan.Before && !effect.Staged.Present && effect.Complete && !effect.Actual.Present {
				owned = true
				break
			}
		}
		if !owned {
			return errors.New("pending target asset has no matching staged effect evidence")
		}
	}
	return nil
}

func (d *linuxInstallDriver) classifyPendingTargetAssets(ctx context.Context, pending linuxInventory) (published, rollbackStarted bool, retErr error) {
	published, rollbackStarted = true, true
	for i, plan := range d.intent.Assets {
		actual, err := inspectLinuxAsset(ctx, plan.Role)
		if err != nil {
			return false, false, err
		}
		state := actual.State()
		_ = actual.Close()
		if state != pending.Assets[i].State {
			published = false
		}
		if plan.Role == linuxAssetUpdateTrust {
			if state.Present && d.validateUpdateTrust(ctx) != nil {
				rollbackStarted = false
			} else if state != pending.Assets[i].State && state != plan.Before {
				rollbackStarted = false
			}
			continue
		}
		if state == plan.Before {
			continue
		}
		if d.intent.PriorInventory != nil {
			prior := rollbackPriorAssetState(*d.intent, plan, i)
			if sameLinuxAssetContent(state, prior) {
				continue
			}
		} else if plan.Role == linuxAssetBootstrapTrust {
			if state == plan.Before || sameLinuxAssetContent(state, plan.After) {
				continue
			}
		} else if !isLinuxDeploymentAsset(plan.Role) {
			continue
		}
		rollbackStarted = false
	}
	return published, rollbackStarted, nil
}

func (d *linuxInstallDriver) validatePendingInventory(ctx context.Context, tx transaction, pending linuxInventory) error {
	if err := validateLinuxInventory(pending); err != nil {
		return err
	}
	principal, _, err := d.evidencePrincipal(ctx)
	if err != nil || principal == nil || pending.Principal != *principal || len(pending.Assets) != len(d.intent.Assets) {
		return errors.New("pending Linux inventory principal or asset set is invalid")
	}
	for i, plan := range d.intent.Assets {
		actual, inspectErr := inspectLinuxAsset(ctx, plan.Role)
		if inspectErr != nil {
			return inspectErr
		}
		state := actual.State()
		closeErr := actual.Close()
		if closeErr != nil || !sameLinuxAssetState(state, pending.Assets[i].State) {
			return errors.Join(errors.New("pending Linux inventory does not describe current assets"), closeErr)
		}
	}
	if tx.Phase == phaseReplacing {
		status, image := "installed", d.intent.Target
		if tx.Mode == modeUninstall {
			status, image = "uninstalled", d.intent.Prior
		}
		if image == nil || pending.Status != status || pending.OperationID != tx.ID || !sameImage(pending.Image, *image) {
			return errors.New("pending Linux inventory does not match the publication phase")
		}
		for _, plan := range d.intent.Assets {
			actual, inspectErr := inspectLinuxAsset(ctx, plan.Role)
			if inspectErr != nil {
				return inspectErr
			}
			state := actual.State()
			_ = actual.Close()
			if !matchesPlannedLinuxAsset(state, plan.After) {
				return errors.New("pending inventory precedes completed asset publication")
			}
		}
		return nil
	}
	if tx.Phase == phaseRollingBack {
		if d.intent.PriorInventory != nil {
			expected := *d.intent.PriorInventory
			if d.intent.Prior != nil {
				expected.Status, expected.Image = "installed", *d.intent.Prior
			}
			for i, plan := range d.intent.Assets {
				actual, inspectErr := inspectLinuxAsset(ctx, plan.Role)
				if inspectErr != nil {
					return inspectErr
				}
				expected.Assets[i].State = actual.State()
				_ = actual.Close()
			}
			if reflect.DeepEqual(expected, pending) {
				return nil
			}
		} else if tx.Mode == modeApply && d.intent.Target != nil && pending.Status == "retained" && pending.OperationID == tx.ID && sameImage(pending.Image, *d.intent.Target) {
			for i, plan := range d.intent.Assets {
				state := pending.Assets[i].State
				if plan.Role != linuxAssetUpdateTrust && plan.Role != linuxAssetBootstrapTrust && state.Present {
					return errors.New("retained pending inventory contains a deployment asset")
				}
				if plan.Role == linuxAssetBootstrapTrust && state.Present && !sameLinuxAssetContent(state, plan.After) {
					return errors.New("retained pending inventory has unproved bootstrap trust")
				}
				if plan.Role == linuxAssetUpdateTrust && state.Present {
					return errors.New("retained pending inventory has unproved update trust")
				}
			}
			return nil
		}
	}
	return errors.New("pending Linux inventory is not bound to a recoverable installer phase")
}

func (d *linuxInstallDriver) recoverResultPending(ctx context.Context, tx transaction) error {
	body, pendingInfo, err := readProtectedPending(ctx, d.operation, linuxResultName+".pending", 4096, d.hooks.testRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if tx.Phase != phaseCommitted && tx.Phase != phaseRolledBack {
		return errors.New("pending terminal result has no terminal transaction")
	}
	result := terminalResult(tx)
	if tx.Phase == phaseCommitted {
		result = committedResult(tx)
	}
	expected, err := json.Marshal(struct {
		Outcome     Outcome `json:"outcome"`
		OperationID string  `json:"operation_id"`
		Image       *Image  `json:"image,omitempty"`
		FailedStage string  `json:"failed_stage"`
	}{result.Outcome, result.OperationID, result.Image, result.FailedStage})
	if err != nil || !bytes.Equal(expected, body) {
		return errors.New("pending terminal result does not match the completed operation")
	}
	if _, err := d.operation.Lstat(linuxResultName); err == nil {
		return errors.New("pending terminal result conflicts with an existing result")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return promoteProtectedPending(ctx, d.operation, linuxResultName+".pending", linuxResultName, body, pendingInfo, nil, nil, d.hooks.testRoot)
}
