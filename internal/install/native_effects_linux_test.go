//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNativeEffectJournalRoundTripsExactStagedIdentity(t *testing.T) {
	base := t.TempDir()
	opPath := filepath.Join(base, strings.Repeat("c", 64))
	if err := os.Mkdir(opPath, 0700); err != nil {
		t.Fatal(err)
	}
	op, err := os.OpenRoot(opPath)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	id := strings.Repeat("c", 64)
	image := Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("d", 64), Target: &image}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phasePrepared, Target: &image}
	d := &linuxInstallDriver{operation: op, intent: &intent, tx: &tx, hooks: linuxNativeHooks{testRoot: true}}
	ctx := context.Background()
	if err := d.writeInitialEffects(ctx); err != nil {
		t.Fatal(err)
	}
	content := []byte("private staged manager image")
	file, err := op.OpenFile("staged", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("staged fixture has no Linux inode identity")
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	staged := linuxAssetState{Present: true, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)), Mode: 0755, Device: uint64(stat.Dev), Inode: stat.Ino}
	if err := d.recordEffect(ctx, linuxAssetManager, "publish", linuxAssetState{}, staged); err != nil {
		t.Fatal(err)
	}
	evidence, _, _, err := d.readEvidence(ctx)
	if err != nil || len(evidence.Effects) != 1 || evidence.Effects[0].Staged != staged || evidence.Effects[0].Complete {
		t.Fatalf("effect evidence=%+v err=%v", evidence, err)
	}
	if err := d.completeEffect(ctx, linuxAssetManager, "publish", staged); err != nil {
		t.Fatal(err)
	}
	evidence, _, _, err = d.readEvidence(ctx)
	if err != nil || len(evidence.Effects) != 1 || !evidence.Effects[0].Complete || evidence.Effects[0].Actual != staged {
		t.Fatalf("completed effect evidence=%+v err=%v", evidence, err)
	}
}

func TestRecoverPendingUpdaterReconciliationMarker(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, strings.Repeat("c", 64))
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	op, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer op.Close()
	id := strings.Repeat("c", 64)
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("d", 64)}
	d := &linuxInstallDriver{operation: op, intent: &intent, hooks: linuxNativeHooks{testRoot: true}}
	if err := d.writeInitialEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := linuxNativeEvidence{Format: 1, OperationID: id, UpdaterReconciled: true, Effects: []linuxNativeEffect{}}
	body, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := op.OpenFile("effects.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pending.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := pending.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := pending.Close(); err != nil {
		t.Fatal(err)
	}
	if err := d.recoverEffectsPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	evidence, _, _, err := d.readEvidence(context.Background())
	if err != nil || !evidence.UpdaterReconciled {
		t.Fatalf("recovered evidence=%+v err=%v", evidence, err)
	}
	if _, err := op.Lstat("effects.pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending evidence remains after promotion: %v", err)
	}
}

func TestRetainedInventoryOriginValidatesIntentWithoutInventingTerminalStage(t *testing.T) {
	ctx := context.Background()
	statePath := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	store, err := newJournalStoreWithConfig(ctx, state, journalStoreConfig{testRoot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	intent, prepared := freshLinuxApplyFixture()
	rawIntent, err := marshalLinuxIntent(intent, prepared)
	if err != nil {
		t.Fatal(err)
	}
	prepared.IntentSHA256, err = store.CreateIntent(ctx, prepared.ID, rawIntent)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := openLinuxOperation(ctx, state, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	principal := linuxPrincipal{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	d := &linuxInstallDriver{
		state: state, store: store, operation: operation, intent: &intent,
		hooks: linuxNativeHooks{stateRoot: statePath, testRoot: true},
	}
	if err := d.writeInitialEffects(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.setExistingPrincipal(ctx, principal); err != nil {
		t.Fatal(err)
	}
	evidence, _, evidenceInfo, err := d.readEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	evidence.AccountCreated = true
	evidenceBody, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.writeEvidence(ctx, evidenceBody, evidenceInfo); err != nil {
		t.Fatal(err)
	}

	rolledBack := prepared
	rolledBack.Phase = phaseRolledBack
	rolledBack.FailedStage = "verify"
	terminal := terminalResult(rolledBack)
	resultBody, err := json.Marshal(struct {
		Outcome     Outcome `json:"outcome"`
		OperationID string  `json:"operation_id"`
		Image       *Image  `json:"image,omitempty"`
		FailedStage string  `json:"failed_stage"`
	}{terminal.Outcome, terminal.OperationID, terminal.Image, terminal.FailedStage})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.FailedStage != "" {
		t.Fatalf("protected terminal result unexpectedly preserved worker stage: %+v", terminal)
	}
	if err := writeLinuxProtectedState(ctx, operation, linuxResultName, resultBody, nil, true); err != nil {
		t.Fatal(err)
	}

	// The terminal result intentionally omits the worker's original failure
	// stage, so a synthetic rolled-back transaction cannot validate the
	// immutable intent. This reproduces the retained-check regression.
	rolledBack.IntentSHA256 = prepared.IntentSHA256
	rolledBack.FailedStage = ""
	if _, err := decodeLinuxIntent(rawIntent, rolledBack); err == nil {
		t.Fatal("rolled-back binding without a persisted failure stage unexpectedly validated")
	}

	assets := make([]linuxOwnedAsset, 0, len(fixedLinuxAssets))
	for _, spec := range fixedLinuxAssets {
		assets = append(assets, linuxOwnedAsset{Role: spec.Role})
	}
	inventory := linuxInventory{
		Format: 1, Status: "retained", OperationID: prepared.ID,
		Image: *intent.Target, Principal: principal, Assets: assets,
	}
	if _, err := d.retainedInventoryOrigin(ctx, inventory); err != nil {
		t.Fatalf("valid rolled-back first-install origin was rejected: %v", err)
	}

	unexpectedImage := *intent.Target
	resultWithImage, err := json.Marshal(struct {
		Outcome     Outcome `json:"outcome"`
		OperationID string  `json:"operation_id"`
		Image       *Image  `json:"image,omitempty"`
		FailedStage string  `json:"failed_stage"`
	}{OutcomeRolledBack, prepared.ID, &unexpectedImage, ""})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(statePath, installDirectory, prepared.ID, linuxResultName), resultWithImage, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.retainedInventoryOrigin(ctx, inventory); err == nil {
		t.Fatal("fresh-install retained origin accepted a rolled-back result with an unexpected image")
	}
}

func TestRollbackAssetRestoreAcceptsNewInodeWithCapturedContent(t *testing.T) {
	snapshot, target, operation := linuxAssetFixture(t, linuxAssetManager, true)
	before := snapshot.State()
	// Keep the original inode allocated after the driver replaces its snapshot
	// with the candidate. Without this pin, unlinking the candidate can allow
	// immediate reuse of the preimage inode number on some filesystems.
	preimagePin, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer preimagePin.Close()
	preimageInfo, err := preimagePin.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if uint64(preimageInfo.Sys().(*syscall.Stat_t).Ino) != before.Inode {
		t.Fatal("preimage pin does not refer to captured asset inode")
	}
	if err := captureLinuxAsset(context.Background(), snapshot, operation); err != nil {
		t.Fatal(err)
	}
	priorImage := Image{Version: "v1.2.2", Schema: 18, SHA256: strings.Repeat("a", 64)}
	targetImage := Image{Version: "v1.2.3", Schema: 18, SHA256: strings.Repeat("b", 64)}
	plan := linuxAssetPlan{Role: linuxAssetManager, Before: before, After: linuxAssetNext([]byte("new candidate"), 0755)}
	id := strings.Repeat("e", 64)
	if !validLinuxAssetOperationID(id) || !validLinuxAssetNext(plan.Role, plan.After) || !snapshot.parentComplete {
		t.Fatalf("fixture publication setup invalid: id=%v next=%v complete=%v role=%q state=%+v", validLinuxAssetOperationID(id), validLinuxAssetNext(plan.Role, plan.After), snapshot.parentComplete, plan.Role, plan.After)
	}
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("f", 64), Prior: &priorImage, Target: &targetImage, Assets: []linuxAssetPlan{plan}}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phasePrepared, IntentSHA256: strings.Repeat("1", 64), Prior: &priorImage, Target: &targetImage}
	d := &linuxInstallDriver{operation: operation, intent: &intent, tx: &tx, assets: map[linuxAssetRole]*linuxAssetSnapshot{linuxAssetManager: snapshot}, hooks: linuxNativeHooks{testRoot: true}}
	if err := d.writeInitialEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	content := []byte("new candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "candidate-source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := publishLinuxAssetRecorded(context.Background(), id, snapshot, source, plan.After, func(staged linuxAssetState) error {
		return d.recordEffect(context.Background(), plan.Role, "publish", before, staged)
	})
	closeErr := source.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("publish candidate: %v %v", err, closeErr)
	}
	if err := d.completeEffect(context.Background(), plan.Role, "publish", actual); err != nil {
		t.Fatal(err)
	}
	candidateSnapshot, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.replaceAssetSnapshot(linuxAssetManager, candidateSnapshot)
	if err := d.rollbackAssets(context.Background(), tx, intent); err != nil {
		t.Fatal(err)
	}
	restored, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	state := restored.State()
	if !sameLinuxAssetContent(state, before) || state.Inode == before.Inode || state.Device != before.Device {
		t.Fatalf("restored asset state=%+v before=%+v; restore must preserve content and accept its newly published inode", state, before)
	}
}

func TestRollbackPreservesUnrecordedSameTargetReplacement(t *testing.T) {
	snapshot, target, operation := linuxAssetFixture(t, linuxAssetManager, true)
	before := snapshot.State()
	priorImage := Image{Version: "v1.2.2", Schema: 18, SHA256: strings.Repeat("a", 64)}
	targetImage := Image{Version: "v1.2.3", Schema: 18, SHA256: strings.Repeat("b", 64)}
	candidate := []byte("same target bytes, foreign inode")
	plan := linuxAssetPlan{Role: linuxAssetManager, Before: before, After: linuxAssetNext(candidate, 0755)}
	id := strings.Repeat("9", 64)
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("8", 64), Prior: &priorImage, Target: &targetImage, Assets: []linuxAssetPlan{plan}}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phaseRollingBack, IntentSHA256: strings.Repeat("1", 64), Prior: &priorImage, Target: &targetImage}
	d := &linuxInstallDriver{operation: operation, intent: &intent, tx: &tx, assets: map[linuxAssetRole]*linuxAssetSnapshot{linuxAssetManager: snapshot}, hooks: linuxNativeHooks{testRoot: true}}
	if err := d.writeInitialEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, candidate, 0755); err != nil {
		t.Fatal(err)
	}
	current, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	foreign := current.State()
	_ = current.Close()
	if foreign.Inode == before.Inode || !sameLinuxAssetContent(foreign, plan.After) {
		t.Fatalf("fixture did not create a same-content replacement inode: before=%+v foreign=%+v", before, foreign)
	}
	if err := d.rollbackAssets(context.Background(), tx, intent); err == nil {
		t.Fatal("rollback accepted a same-target replacement without recorded installer ownership")
	}
	stillThere, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(stillThere, candidate) {
		t.Fatalf("rollback modified the unrecorded replacement: bytes=%q err=%v", stillThere, err)
	}
}

func TestRollbackPriorManagerUsesCapturedSupersededReceiptSize(t *testing.T) {
	priorInventory := linuxInventoryFixture("installed")
	priorInventory.Assets[0].State.Size = 7 // inventory still describes manager image A.
	priorImage := Image{Version: "v1.2.4", Schema: priorInventory.Image.Schema, SHA256: strings.Repeat("b", 64)}
	capturedManager := linuxAssetState{Present: true, SHA256: priorImage.SHA256, Size: 2048, Mode: 0755, Device: 8, Inode: 99}
	intent := linuxIntent{Prior: &priorImage, PriorInventory: &priorInventory}
	plan := linuxAssetPlan{Role: linuxAssetManager, Before: capturedManager}
	expected := rollbackPriorAssetState(intent, plan, 0)
	if !sameLinuxAssetContent(expected, capturedManager) || expected.Size != 2048 {
		t.Fatalf("rollback manager expectation=%+v captured receipt asset=%+v", expected, capturedManager)
	}
}

func TestRollbackAssetsRebindsSnapshotsForPriorInventoryRestoration(t *testing.T) {
	base := t.TempDir()
	assetRoot := filepath.Join(base, "assets")
	if err := os.Mkdir(assetRoot, 0700); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string, len(fixedLinuxAssets))
	for _, spec := range fixedLinuxAssets {
		paths[spec.Role] = filepath.Join(assetRoot, string(spec.Role), filepath.Base(spec.Path))
	}
	previous := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: assetRoot, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = previous })

	operationPath := filepath.Join(base, "operation")
	if err := os.Mkdir(operationPath, 0700); err != nil {
		t.Fatal(err)
	}
	operation, err := os.OpenRoot(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	statePath := filepath.Join(base, "state")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer stateRoot.Close()
	intent, tx := retainedLinuxApplyFixture()
	tx.Phase = phaseRollingBack
	for i := range intent.Assets {
		intent.Assets[i].Before = linuxAssetState{}
		intent.PriorInventory.Assets[i].State = linuxAssetState{}
	}
	d := &linuxInstallDriver{state: stateRoot, operation: operation, intent: &intent, tx: &tx, assets: make(map[linuxAssetRole]*linuxAssetSnapshot), hooks: linuxNativeHooks{testRoot: true}}
	if err := d.writeInitialEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.rollbackAssets(context.Background(), tx, intent); err != nil {
		t.Fatalf("rollback of unchanged captured assets: %v", err)
	}
	for _, plan := range intent.Assets {
		snapshot := d.assets[plan.Role]
		if snapshot == nil {
			t.Fatalf("independent rollback did not retain current snapshot for %s", plan.Role)
		}
		current, err := currentLinuxAsset(context.Background(), snapshot)
		if err != nil || current != plan.Before {
			t.Fatalf("retained snapshot for %s has state %+v, want %+v: %v", plan.Role, current, plan.Before, err)
		}
	}
	if err := d.restorePriorInventory(context.Background(), tx, intent); err == nil || err.Error() != "Linux inventory is not the exact operation image being rolled back" {
		t.Fatalf("prior inventory restore did not advance to its expected inventory CAS: %v", err)
	}
}

func TestRestorePriorInventoryReturnsToUninstalledStateAfterFailedReinstall(t *testing.T) {
	ctx := context.Background()
	d, prior, closeFixture := newLinuxInventoryRestoreDriver(t, "uninstalled")
	defer closeFixture()

	intent, tx := managedLinuxApplyFixture()
	prior.OperationID = strings.Repeat("9", 64)
	priorOriginal := cloneLinuxInventoryFixture(prior)
	intent.PriorInventory = &prior
	intent.Prior = imagePointer(prior.Image)
	intent.Principal = &prior.Principal
	intent.ServiceWasActive = false
	intent.ServiceWasEnabled = false
	tx.ID = strings.Repeat("8", 64)
	intent.ID = tx.ID
	tx.Prior = imagePointer(prior.Image)
	intent.Target = tx.Target
	for i := range intent.Assets {
		intent.Assets[i].Before = prior.Assets[i].State
	}

	if err := d.publishInventory(ctx, prior, nil); err != nil {
		t.Fatal(err)
	}
	if err := d.restorePriorInventory(ctx, tx, intent); err != nil {
		t.Fatalf("restore captured uninstalled inventory after failed reinstall: %v", err)
	}
	after, present, err := d.readInventory(ctx)
	if err != nil || !present || after.Status != "uninstalled" || !sameImage(after.Image, prior.Image) {
		t.Fatalf("restored inventory = %+v, present=%v err=%v", after, present, err)
	}
	if !reflect.DeepEqual(priorOriginal, *intent.PriorInventory) {
		t.Fatal("rollback mutated immutable captured uninstalled inventory")
	}
}

func TestRollbackFromUninstalledSkipsEnablementOnlyForExactAbsentUnit(t *testing.T) {
	intent, tx := managedLinuxApplyFixture()
	prior := linuxInventoryFixture("uninstalled")
	intent.PriorInventory = &prior
	intent.Prior = imagePointer(prior.Image)
	tx.Prior = imagePointer(prior.Image)
	enableCalls := 0
	d := &linuxInstallDriver{hooks: linuxNativeHooks{
		service: func(context.Context) (linuxServiceStatus, error) {
			return linuxServiceStatus{LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}, nil
		},
		enabled: func(context.Context, bool) error {
			enableCalls++
			return errors.New("must not call enablement for a captured absent unit")
		},
	}}
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("unexpected host service command in rollback fixture: %s %v", name, args)
			return nil, errors.New("unexpected host service command")
		},
	})
	if err := d.restoreManagerEnablementAfterRollback(ctx, tx, intent); err != nil {
		t.Fatalf("rollback of reinstall from an uninstalled state: %v", err)
	}
	if enableCalls != 0 {
		t.Fatalf("rollback called enablement for absent unit: calls=%d", enableCalls)
	}
}

func TestRollbackFromUninstalledRejectsUnexpectedLoadedManagerUnit(t *testing.T) {
	d := &linuxInstallDriver{hooks: linuxNativeHooks{
		service: func(context.Context) (linuxServiceStatus, error) {
			return linuxServiceStatus{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "disabled"}, nil
		},
		enabled: func(context.Context, bool) error {
			t.Fatal("rollback changed enablement for an unexpected loaded unit")
			return nil
		},
	}}
	intent, tx := managedLinuxApplyFixture()
	prior := linuxInventoryFixture("uninstalled")
	intent.PriorInventory = &prior
	intent.Prior = imagePointer(prior.Image)
	tx.Prior = imagePointer(prior.Image)
	ctx := withLinuxServiceTestHooks(context.Background(), linuxServiceTestHooks{
		run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			t.Fatalf("unexpected host service command in rollback fixture: %s %v", name, args)
			return nil, errors.New("unexpected host service command")
		},
	})
	if err := d.restoreManagerEnablementAfterRollback(ctx, tx, intent); err == nil {
		t.Fatal("rollback accepted an unexpected loaded manager unit")
	}
}

func TestRestorePriorInventoryDoesNotMutateCapturedPreimageBeforeCAS(t *testing.T) {
	ctx := context.Background()
	d, prior, closeFixture := newLinuxInventoryRestoreDriver(t, "installed")
	defer closeFixture()

	intent, tx := managedLinuxApplyFixture()
	prior.OperationID = strings.Repeat("9", 64)
	prior.Image.SHA256 = prior.Assets[0].State.SHA256
	priorOriginal := cloneLinuxInventoryFixture(prior)
	intent.PriorInventory = &prior
	intent.Prior = imagePointer(prior.Image)
	intent.Principal = &prior.Principal
	intent.ServiceWasActive = false
	intent.ServiceWasEnabled = false
	tx.ID = strings.Repeat("8", 64)
	intent.ID = tx.ID
	tx.Prior = imagePointer(prior.Image)
	intent.Target = tx.Target
	for i := range intent.Assets {
		intent.Assets[i].Before = prior.Assets[i].State
	}
	if err := d.publishInventory(ctx, prior, nil); err != nil {
		t.Fatal(err)
	}

	// Keep each old inode allocated while linking an exact-content replacement,
	// so the fixture proves that rollback updates recorded identities.
	for _, spec := range fixedLinuxAssets {
		snapshot := d.assets[spec.Role]
		if snapshot == nil || !snapshot.State().Present {
			continue
		}
		oldPath := snapshot.path
		if err := snapshot.Close(); err != nil {
			t.Fatal(err)
		}
		oldCopy := oldPath + ".held-old"
		if err := os.Rename(oldPath, oldCopy); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(oldCopy)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(oldPath, body, os.FileMode(spec.Mode)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(oldPath, os.FileMode(spec.Mode)); err != nil {
			t.Fatal(err)
		}
		rebound, err := inspectLinuxAsset(ctx, spec.Role)
		if err != nil {
			t.Fatal(err)
		}
		if rebound.State().Inode == prior.Assets[assetIndexForRole(spec.Role)].State.Inode {
			_ = rebound.Close()
			t.Fatalf("replacement %s unexpectedly reused the captured inode", spec.Role)
		}
		d.assets[spec.Role] = rebound
	}

	if err := d.restorePriorInventory(ctx, tx, intent); err != nil {
		t.Fatalf("restore prior installed inventory after same-content inode replacement: %v", err)
	}
	after, present, err := d.readInventory(ctx)
	if err != nil || !present || after.Status != "installed" || !sameImage(after.Image, prior.Image) {
		t.Fatalf("restored inventory = %+v, present=%v err=%v", after, present, err)
	}
	if !reflect.DeepEqual(priorOriginal, *intent.PriorInventory) {
		t.Fatal("rollback mutated immutable captured installed inventory before inventory CAS")
	}
	for i, spec := range fixedLinuxAssets {
		if after.Assets[i].State != d.assets[spec.Role].State() {
			t.Fatalf("restored inventory state for %s = %+v, actual %+v", spec.Role, after.Assets[i].State, d.assets[spec.Role].State())
		}
	}
}

func newLinuxInventoryRestoreDriver(t *testing.T, status string) (*linuxInstallDriver, linuxInventory, func()) {
	t.Helper()
	base := t.TempDir()
	assetRoot := filepath.Join(base, "assets")
	if err := os.Mkdir(assetRoot, 0700); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string, len(fixedLinuxAssets))
	for _, spec := range fixedLinuxAssets {
		path := filepath.Join(assetRoot, string(spec.Role), filepath.Base(spec.Path))
		paths[spec.Role] = path
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if status == "installed" && spec.Role != linuxAssetUpdateTrust || spec.Role == linuxAssetBootstrapTrust {
			body := []byte("rollback inventory fixture: " + string(spec.Role))
			if spec.Role == linuxAssetBootstrapTrust {
				body = linuxTrustFixture()
			}
			if err := os.WriteFile(path, body, os.FileMode(spec.Mode)); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, os.FileMode(spec.Mode)); err != nil {
				t.Fatal(err)
			}
		}
	}
	previous := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: assetRoot, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = previous })

	statePath := filepath.Join(base, "state")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	state, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	d := &linuxInstallDriver{state: state, hooks: linuxNativeHooks{stateRoot: statePath, dataRoot: filepath.Join(base, "data"), testRoot: true}, assets: make(map[linuxAssetRole]*linuxAssetSnapshot)}
	inventory := linuxInventory{Format: 1, Status: status, OperationID: strings.Repeat("7", 64), Image: linuxImage("a"), Principal: linuxPrincipal{UID: 1000, GID: 1000}, Assets: make([]linuxOwnedAsset, len(fixedLinuxAssets))}
	for i, spec := range fixedLinuxAssets {
		snapshot, err := inspectLinuxAsset(context.Background(), spec.Role)
		if err != nil {
			_ = state.Close()
			t.Fatal(err)
		}
		d.assets[spec.Role] = snapshot
		inventory.Assets[i] = linuxOwnedAsset{Role: spec.Role, State: snapshot.State()}
	}
	if status == "installed" {
		inventory.Image.SHA256 = inventory.Assets[0].State.SHA256
	}
	closeFixture := func() {
		for _, snapshot := range d.assets {
			_ = snapshot.Close()
		}
		_ = state.Close()
	}
	return d, inventory, closeFixture
}

func assetIndexForRole(role linuxAssetRole) int {
	for i, spec := range fixedLinuxAssets {
		if spec.Role == role {
			return i
		}
	}
	return -1
}
