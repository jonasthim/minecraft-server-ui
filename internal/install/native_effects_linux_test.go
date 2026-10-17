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
