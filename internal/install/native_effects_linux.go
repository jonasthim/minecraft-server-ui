//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

type linuxNativeEffect struct {
	Role            linuxAssetRole  `json:"role"`
	Direction       string          `json:"direction"`
	StagingProtocol *string         `json:"staging_protocol,omitempty"`
	Before          linuxAssetState `json:"before"`
	Staged          linuxAssetState `json:"staged"`
	Actual          linuxAssetState `json:"actual"`
	Complete        bool            `json:"complete"`
}

const linuxAssetStagingAnonymousV1 = "anonymous-v1"

type linuxNativeEvidence struct {
	Format            int                 `json:"format"`
	OperationID       string              `json:"operation_id"`
	Principal         *linuxPrincipal     `json:"principal,omitempty"`
	AccountCreated    bool                `json:"account_created"`
	UpdaterReconciled bool                `json:"updater_reconciled"`
	RollbackNonce     string              `json:"rollback_nonce"`
	Effects           []linuxNativeEffect `json:"effects"`
}

func (d *linuxInstallDriver) writeInitialEffects(ctx context.Context) error {
	if d.operation == nil || d.intent == nil {
		return errors.New("installer operation evidence is unavailable")
	}
	evidence := linuxNativeEvidence{Format: 1, OperationID: d.intent.ID, Principal: d.intent.Principal, Effects: []linuxNativeEffect{}}
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return d.writeEvidence(ctx, body, nil)
}

func (d *linuxInstallDriver) readEvidence(ctx context.Context) (linuxNativeEvidence, []byte, os.FileInfo, error) {
	var evidence linuxNativeEvidence
	if d.operation == nil || ctx == nil || ctx.Err() != nil {
		return evidence, nil, nil, errors.New("installer evidence is unavailable")
	}
	info, err := d.operation.Lstat(linuxEffectsName)
	if err != nil {
		return evidence, nil, nil, err
	}
	if !linuxNativeProtectedFile(info, d.hooks.testRoot) || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > linuxMaxEffectsBytes {
		return evidence, nil, nil, errors.New("installer evidence file is unsafe")
	}
	file, err := d.operation.Open(linuxEffectsName)
	if err != nil {
		return evidence, nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return evidence, nil, nil, errors.New("installer evidence identity changed")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, linuxMaxEffectsBytes+1))
	closeErr := file.Close()
	after, statErr := d.operation.Lstat(linuxEffectsName)
	if readErr != nil || closeErr != nil || statErr != nil || !os.SameFile(info, after) || len(body) != int(info.Size()) {
		return evidence, nil, nil, errors.Join(errors.New("installer evidence read failed"), readErr, closeErr, statErr)
	}
	if err := decodeLinuxStrictJSON(body, linuxMaxEffectsBytes, &evidence); err != nil || evidence.Format != 1 || d.intent == nil || evidence.OperationID != d.intent.ID {
		return evidence, nil, nil, errors.New("installer evidence is malformed")
	}
	if len(evidence.Effects) > 128 {
		return evidence, nil, nil, errors.New("installer evidence exceeds its bound")
	}
	for _, effect := range evidence.Effects {
		if !validLinuxNativeEffect(effect) {
			return evidence, nil, nil, errors.New("installer native effect record is invalid")
		}
	}
	return evidence, body, info, nil
}

func validLinuxNativeEffect(effect linuxNativeEffect) bool {
	protocol := linuxEffectStagingProtocol(effect)
	if effect.StagingProtocol != nil && protocol != linuxAssetStagingAnonymousV1 {
		return false
	}
	if _, ok := linuxAssetSpecForRole(effect.Role); !ok || (effect.Direction != "publish" && effect.Direction != "remove") || !validLinuxCapturedState(effect.Role, effect.Before) || !validLinuxCapturedState(effect.Role, effect.Staged) || !validLinuxCapturedState(effect.Role, effect.Actual) || (protocol != "" && (effect.Direction != "publish" || protocol != linuxAssetStagingAnonymousV1)) {
		return false
	}
	if effect.Direction == "publish" {
		return effect.Staged.Present && effect.Staged.Device != 0 && effect.Staged.Inode != 0
	}
	return !effect.Staged.Present && effect.StagingProtocol == nil
}

func sameLinuxNativeEffect(a, b linuxNativeEffect) bool {
	return a.Role == b.Role && a.Direction == b.Direction && linuxEffectProtocolEqual(a, b) && a.Before == b.Before && a.Staged == b.Staged && a.Actual == b.Actual && a.Complete == b.Complete
}

func linuxEffectProtocolEqual(a, b linuxNativeEffect) bool {
	if (a.StagingProtocol == nil) != (b.StagingProtocol == nil) {
		return false
	}
	return linuxEffectStagingProtocol(a) == linuxEffectStagingProtocol(b)
}

func linuxEffectStagingProtocol(effect linuxNativeEffect) string {
	if effect.StagingProtocol == nil {
		return ""
	}
	return *effect.StagingProtocol
}

func (d *linuxInstallDriver) writeEvidence(ctx context.Context, body []byte, priorInfo os.FileInfo) error {
	if d.operation == nil || len(body) == 0 || len(body) > linuxMaxEffectsBytes {
		return errors.New("installer evidence encoding is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, currentErr := d.operation.Lstat(linuxEffectsName)
	if priorInfo == nil {
		if currentErr == nil {
			return errors.New("installer evidence already exists")
		}
		if !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
	} else if currentErr != nil || !os.SameFile(priorInfo, current) {
		return errors.New("installer evidence changed before publication")
	}
	if _, err := d.operation.Lstat("effects.pending"); err == nil {
		return errors.New("installer evidence pending slot is occupied")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	pending, err := d.operation.OpenFile("effects.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	created, statErr := pending.Stat()
	if statErr != nil || !linuxNativeProtectedFile(created, d.hooks.testRoot) || created.Mode().Perm() != 0600 {
		_ = pending.Close()
		return errors.Join(errors.New("new installer evidence file is unsafe"), statErr)
	}
	writeErr := writeAll(pending, body)
	if writeErr == nil {
		writeErr = pending.Sync()
	}
	closeErr := pending.Close()
	if writeErr != nil || closeErr != nil {
		_ = removeExactEvidencePending(d.operation, created, body)
		return errors.Join(writeErr, closeErr)
	}
	if err := syncDirectory(d.operation); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, currentErr = d.operation.Lstat(linuxEffectsName)
	if priorInfo == nil {
		if currentErr == nil {
			return errors.New("installer evidence appeared before publication")
		}
		if !errors.Is(currentErr, os.ErrNotExist) {
			return currentErr
		}
	} else if currentErr != nil || !os.SameFile(priorInfo, current) {
		return errors.New("installer evidence changed before publication")
	}
	pendingInfo, err := d.operation.Lstat("effects.pending")
	if err != nil || !os.SameFile(created, pendingInfo) {
		return errors.Join(errors.New("installer evidence pending identity changed"), err)
	}
	if err := d.operation.Rename("effects.pending", linuxEffectsName); err != nil {
		return fmt.Errorf("publish installer evidence: %w", err)
	}
	if err := syncDirectory(d.operation); err != nil {
		return fmt.Errorf("sync installer evidence publication: %w", err)
	}
	published, err := d.operation.Lstat(linuxEffectsName)
	if err != nil || !os.SameFile(created, published) {
		return errors.Join(errors.New("installer evidence publication is uncertain"), err)
	}
	return nil
}

func linuxNativeProtectedFile(info os.FileInfo, testRoot bool) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return false
	}
	wantUID := uint32(0)
	if testRoot {
		wantUID = uint32(os.Geteuid())
	}
	return stat.Uid == wantUID
}

func removeExactEvidencePending(root *os.Root, info os.FileInfo, body []byte) error {
	current, err := root.Lstat("effects.pending")
	if err != nil || !os.SameFile(current, info) {
		return errors.Join(errors.New("installer evidence pending slot changed"), err)
	}
	file, err := root.Open("effects.pending")
	if err != nil {
		return err
	}
	currentInfo, err := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, linuxMaxEffectsBytes+1))
	closeErr := file.Close()
	if err != nil || readErr != nil || closeErr != nil || !os.SameFile(info, currentInfo) || !bytes.Equal(raw, body) {
		return errors.Join(errors.New("installer evidence pending bytes changed"), err, readErr, closeErr)
	}
	if err := root.Remove("effects.pending"); err != nil {
		return err
	}
	return syncDirectory(root)
}

func (d *linuxInstallDriver) recordEffect(ctx context.Context, role linuxAssetRole, direction string, before, staged linuxAssetState) error {
	return d.recordEffectProtocol(ctx, role, direction, "", before, staged)
}

func (d *linuxInstallDriver) recordPublishEffect(ctx context.Context, role linuxAssetRole, before, staged linuxAssetState) error {
	return d.recordEffectProtocol(ctx, role, "publish", linuxAssetStagingAnonymousV1, before, staged)
}

func (d *linuxInstallDriver) recordEffectProtocol(ctx context.Context, role linuxAssetRole, direction, protocol string, before, staged linuxAssetState) error {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	var protocolPointer *string
	if protocol != "" {
		protocolPointer = &protocol
	}
	evidence.Effects = append(evidence.Effects, linuxNativeEffect{Role: role, Direction: direction, StagingProtocol: protocolPointer, Before: before, Staged: staged, Actual: linuxAssetState{}})
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return d.writeEvidence(ctx, body, info)
}

// markUpdaterReconciled durably closes the recovery window between updater
// terminal cleanup and native asset publication. Replace may begin only after
// this marker is present; rollback uses it to decide whether a fresh updater
// snapshot is valid for the current manager image.
func (d *linuxInstallDriver) markUpdaterReconciled(ctx context.Context) error {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	if evidence.UpdaterReconciled {
		return nil
	}
	if len(evidence.Effects) != 0 {
		return errors.New("updater reconciliation marker cannot follow native asset effects")
	}
	evidence.UpdaterReconciled = true
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return d.writeEvidence(ctx, body, info)
}

func (d *linuxInstallDriver) completeEffect(ctx context.Context, role linuxAssetRole, direction string, actual linuxAssetState) error {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	for i := len(evidence.Effects) - 1; i >= 0; i-- {
		effect := &evidence.Effects[i]
		if effect.Role == role && effect.Direction == direction && !effect.Complete {
			effect.Actual, effect.Complete = actual, true
			body, err := json.Marshal(evidence)
			if err != nil {
				return err
			}
			return d.writeEvidence(ctx, body, info)
		}
	}
	return errors.New("installer effect completion has no matching intent")
}

func (d *linuxInstallDriver) setCreatedPrincipal(ctx context.Context, principal linuxPrincipal) error {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	if evidence.Principal != nil {
		if !sameLinuxPrincipal(*evidence.Principal, principal) {
			return errors.New("created manager account identity changed")
		}
		return nil
	}
	evidence.Principal = &principal
	evidence.AccountCreated = true
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return d.writeEvidence(ctx, body, info)
}

func (d *linuxInstallDriver) setExistingPrincipal(ctx context.Context, principal linuxPrincipal) error {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return err
	}
	if evidence.Principal != nil {
		if !sameLinuxPrincipal(*evidence.Principal, principal) {
			return errors.New("manager account identity differs from captured evidence")
		}
		return nil
	}
	evidence.Principal = &principal
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	return d.writeEvidence(ctx, body, info)
}

func (d *linuxInstallDriver) evidencePrincipal(ctx context.Context) (*linuxPrincipal, bool, error) {
	evidence, _, _, err := d.readEvidence(ctx)
	if err != nil {
		return nil, false, err
	}
	if evidence.Principal == nil {
		return nil, evidence.AccountCreated, nil
	}
	value := *evidence.Principal
	return &value, evidence.AccountCreated, nil
}

// rollbackNonce returns the nonce persisted for the old manager's fresh
// rollback readiness handshake. A recovery retry reuses only this exact nonce;
// it never invents a second challenge while a first one may still be live.
func (d *linuxInstallDriver) rollbackNonce(ctx context.Context) (string, error) {
	evidence, _, info, err := d.readEvidence(ctx)
	if err != nil {
		return "", err
	}
	if evidence.RollbackNonce != "" {
		if !lowerHex(evidence.RollbackNonce, 64) {
			return "", errors.New("persisted rollback nonce is invalid")
		}
		return evidence.RollbackNonce, nil
	}
	nonce, err := randomLinuxHex(32)
	if err != nil {
		return "", err
	}
	evidence.RollbackNonce = nonce
	body, err := json.Marshal(evidence)
	if err != nil {
		return "", err
	}
	if err := d.writeEvidence(ctx, body, info); err != nil {
		return "", err
	}
	return nonce, nil
}

// retainedInventoryOrigin proves the non-installed retained marker against the
// original immutable first-install intent, terminal rollback result, and native
// account evidence. It never treats the attempted image as observed DB schema.
func (d *linuxInstallDriver) retainedInventoryOrigin(ctx context.Context, inventory linuxInventory) (linuxIntent, error) {
	var empty linuxIntent
	if err := validateLinuxInventory(inventory); err != nil || inventory.Status != "retained" {
		return empty, errors.New("retained failed-install inventory is invalid")
	}
	resultOwner := uint32(0)
	if d.hooks.testRoot {
		resultOwner = uint32(os.Geteuid())
	}
	result, present, err := readLinuxInstallerResultAt(ctx, d.hooks.stateRoot, inventory.OperationID, resultOwner)
	if err != nil || !present || result.OperationID != inventory.OperationID || result.Outcome != OutcomeRolledBack || result.Image != nil || result.FailedStage != "" {
		return empty, errors.Join(errors.New("retained failed-install result is missing or invalid"), err)
	}
	op, err := openLinuxOperation(ctx, d.state, inventory.OperationID)
	if err != nil {
		return empty, err
	}
	defer op.Close()
	info, err := op.Lstat(intentFileName)
	if err != nil || checkProtectedFile(info, resultOwner) != nil || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maxIntentBytes {
		return empty, errors.Join(errors.New("retained origin intent is unsafe"), err)
	}
	file, err := op.Open(intentFileName)
	if err != nil {
		return empty, err
	}
	opened, statErr := file.Stat()
	raw, readErr := io.ReadAll(io.LimitReader(file, maxIntentBytes+1))
	closeErr := file.Close()
	after, pathErr := op.Lstat(intentFileName)
	if statErr != nil || readErr != nil || closeErr != nil || pathErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, after) || int64(len(raw)) != info.Size() {
		return empty, errors.Join(errors.New("retained origin intent changed while reading"), statErr, readErr, closeErr, pathErr)
	}
	var fields linuxIntent
	if err := decodeLinuxStrictJSON(raw, maxIntentBytes, &fields); err != nil {
		return empty, errors.New("retained origin intent is malformed")
	}
	sum := sha256.Sum256(raw)
	// The terminal journal has already been removed, and the protected result
	// above proves that this operation rolled back. Use a valid prepared-phase
	// transaction only as the immutable intent binding for ReadIntent and
	// decodeLinuxIntent; the retained inventory does not preserve the original
	// failure stage, so it cannot reconstruct a valid rolled-back transaction.
	// This synthetic binding is never loaded, published, or treated as evidence
	// of the operation's historical phase.
	tx := transaction{Format: 1, ID: inventory.OperationID, Mode: modeApply, Phase: phasePrepared, Prior: fields.Prior, Target: fields.Target, IntentSHA256: hex.EncodeToString(sum[:])}
	verified, err := d.store.ReadIntent(ctx, tx)
	if err != nil || !bytes.Equal(raw, verified) {
		return empty, errors.Join(errors.New("retained origin intent digest is invalid"), err)
	}
	intent, err := decodeLinuxIntent(verified, tx)
	if err != nil || intent.Mode != modeApply || intent.Prior != nil || intent.PriorInventory != nil || intent.Target == nil || !sameImage(*intent.Target, inventory.Image) || intent.Principal != nil || intent.ServiceWasActive || intent.ServiceWasEnabled {
		return empty, errors.Join(errors.New("retained origin does not prove a fresh failed installation"), err)
	}
	check := &linuxInstallDriver{state: d.state, store: d.store, operation: op, intent: &intent, hooks: d.hooks}
	evidence, _, _, err := check.readEvidence(ctx)
	if err != nil || !evidence.AccountCreated || evidence.Principal == nil || !sameLinuxPrincipal(*evidence.Principal, inventory.Principal) {
		return empty, errors.Join(errors.New("retained origin account evidence does not match inventory"), err)
	}
	for _, effect := range evidence.Effects {
		if !effect.Complete {
			return empty, errors.New("retained origin has an incomplete native effect")
		}
	}
	bootstrap := inventory.Assets[12].State
	if bootstrap.Present {
		hash := sha256.Sum256(intent.BootstrapTrust)
		if bootstrap.SHA256 != hex.EncodeToString(hash[:]) || bootstrap.Size != int64(len(intent.BootstrapTrust)) {
			return empty, errors.New("retained bootstrap trust does not match original trust input")
		}
	}
	if inventory.Assets[13].State.Present {
		return empty, errors.New("retained failed install has an unproved update-trust asset")
	}
	return intent, nil
}
