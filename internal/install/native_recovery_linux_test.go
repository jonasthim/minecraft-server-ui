//go:build linux

package install

import (
	"bytes"
	"context"
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

func TestTerminalOperationDirectoryRejectsPendingAndUnknownResidue(t *testing.T) {
	for _, name := range []string{"result.json.pending", "effects.pending", "foreign.json"} {
		t.Run(name, func(t *testing.T) {
			path := t.TempDir()
			root, err := os.OpenRoot(path)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			for _, required := range []string{intentFileName, linuxEffectsName, linuxResultName} {
				if err := os.WriteFile(filepath.Join(path, required), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(path, name), []byte("pending"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := validateTerminalOperationContents(context.Background(), root, true); err == nil {
				t.Fatal("accepted terminal operation directory with pending or unknown residue")
			}
			if _, err := root.Lstat(name); err != nil {
				t.Fatalf("inspection modified residue: %v", err)
			}
		})
	}
}

func TestRecoverPendingTerminalResultRequiresExactTerminalTransaction(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	id := strings.Repeat("a", 64)
	prior := Image{Version: "v1.2.2", Schema: 18, SHA256: strings.Repeat("b", 64)}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phaseRolledBack, IntentSHA256: strings.Repeat("c", 64), Prior: &prior, Target: imagePointer(Image{Version: "v1.2.3", Schema: 18, SHA256: strings.Repeat("d", 64)})}
	d := &linuxInstallDriver{operation: root, hooks: linuxNativeHooks{testRoot: true}}
	expectedResult := terminalResult(tx)
	body, err := json.Marshal(struct {
		Outcome     Outcome `json:"outcome"`
		OperationID string  `json:"operation_id"`
		Image       *Image  `json:"image,omitempty"`
		FailedStage string  `json:"failed_stage"`
	}{expectedResult.Outcome, expectedResult.OperationID, expectedResult.Image, expectedResult.FailedStage})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, linuxResultName+".pending"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.recoverResultPending(context.Background(), tx); err != nil {
		t.Fatal(err)
	}
	actual, err := root.ReadFile(linuxResultName)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("recovered result=%q err=%v", actual, err)
	}
	if _, err := root.Lstat(linuxResultName + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("result pending remains: %v", err)
	}

	foreign := []byte(`{"outcome":"installed","operation_id":"foreign"}`)
	if err := os.WriteFile(filepath.Join(path, linuxResultName+".pending"), foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(linuxResultName); err != nil {
		t.Fatal(err)
	}
	if err := d.recoverResultPending(context.Background(), tx); err == nil {
		t.Fatal("accepted foreign terminal result bytes")
	}
	preserved, err := root.ReadFile(linuxResultName + ".pending")
	if err != nil || !bytes.Equal(preserved, foreign) {
		t.Fatalf("foreign result pending was changed: %q err=%v", preserved, err)
	}
}

func TestRecoverPendingInventoryPromotionAndRollbackDiscard(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "promote-published"
		if rollback {
			name = "discard-after-rollback-started"
		}
		t.Run(name, func(t *testing.T) {
			d, tx, pending, path, stateRoot, cleanup := pendingInventoryFixture(t, rollback)
			defer cleanup()
			body, err := marshalLinuxInventory(pending)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stateRoot, "installation.json.pending"), body, 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.recoverInventoryPending(context.Background(), tx); err != nil {
				t.Fatal(err)
			}
			if rollback {
				if _, err := os.Lstat(filepath.Join(stateRoot, "installation.json.pending")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rolled back target pending remains: %v", err)
				}
			} else {
				actual, present, err := d.readInventory(context.Background())
				if err != nil || !present || !reflect.DeepEqual(actual, pending) {
					t.Fatalf("recovered inventory=%+v present=%v err=%v", actual, present, err)
				}
			}
			_ = path
		})
	}
}

func pendingInventoryFixture(t *testing.T, rollback bool) (*linuxInstallDriver, transaction, linuxInventory, string, string, func()) {
	t.Helper()
	base := t.TempDir()
	assetsRoot := filepath.Join(base, "assets")
	if err := os.Mkdir(assetsRoot, 0755); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string, len(fixedLinuxAssets))
	states := make([]linuxAssetState, len(fixedLinuxAssets))
	var managerHash string
	for i, spec := range fixedLinuxAssets {
		dir := filepath.Join(assetsRoot, string(spec.Role))
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, filepath.Base(spec.Path))
		paths[spec.Role] = path
		if spec.Role == linuxAssetUpdateTrust {
			continue
		}
		content := []byte("candidate asset " + string(spec.Role))
		if err := os.WriteFile(path, content, os.FileMode(spec.Mode)); err != nil {
			t.Fatal(err)
		}
		state := linuxAssetNext(content, spec.Mode)
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		state.Device, state.Inode = linuxTestFileIdentity(info)
		states[i] = state
		if spec.Role == linuxAssetManager {
			managerHash = state.SHA256
		}
	}
	oldPolicy := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: assetsRoot, uid: uint32(os.Geteuid()), paths: paths}
	cleanupAssets := func() { linuxAssetTestPolicy = oldPolicy }
	statePath := filepath.Join(base, "state")
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot, err := os.OpenRoot(statePath)
	if err != nil {
		t.Fatal(err)
	}
	operationPath := filepath.Join(base, "operation")
	if err := os.Mkdir(operationPath, 0700); err != nil {
		t.Fatal(err)
	}
	operation, err := os.OpenRoot(operationPath)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("e", 64)
	image := Image{Version: "v1.2.3", Schema: 18, SHA256: managerHash}
	principal := linuxPrincipal{UID: uint32(os.Geteuid()), GID: uint32(os.Getegid())}
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), Nonce: strings.Repeat("f", 64), Target: &image, Principal: &principal}
	for i, spec := range fixedLinuxAssets {
		after := states[i]
		after.Device, after.Inode = 0, 0
		intent.Assets = append(intent.Assets, linuxAssetPlan{Role: spec.Role, After: after})
	}
	phase := phaseReplacing
	if rollback {
		phase = phaseRollingBack
	}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phase, IntentSHA256: strings.Repeat("1", 64), Target: &image}
	d := &linuxInstallDriver{state: stateRoot, operation: operation, intent: &intent, tx: &tx, assets: make(map[linuxAssetRole]*linuxAssetSnapshot), hooks: linuxNativeHooks{stateRoot: statePath, testRoot: true}}
	if err := d.writeInitialEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.setExistingPrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	for _, plan := range intent.Assets {
		if !plan.After.Present {
			continue
		}
		actual, err := inspectLinuxAsset(context.Background(), plan.Role)
		if err != nil {
			t.Fatal(err)
		}
		state := actual.State()
		_ = actual.Close()
		if err := d.recordEffect(context.Background(), plan.Role, "publish", plan.Before, state); err != nil {
			t.Fatal(err)
		}
		if err := d.completeEffect(context.Background(), plan.Role, "publish", state); err != nil {
			t.Fatal(err)
		}
	}
	assets := make([]linuxOwnedAsset, len(fixedLinuxAssets))
	for i, spec := range fixedLinuxAssets {
		assets[i] = linuxOwnedAsset{Role: spec.Role, State: states[i]}
	}
	pending := linuxInventory{Format: 1, Status: "installed", OperationID: id, Image: image, Principal: principal, Assets: assets}
	if rollback {
		for _, plan := range intent.Assets {
			if !isLinuxDeploymentAsset(plan.Role) {
				continue
			}
			if err := os.Remove(paths[plan.Role]); err != nil {
				t.Fatal(err)
			}
		}
	}
	cleanup := func() {
		cleanupAssets()
		_ = operation.Close()
		_ = stateRoot.Close()
	}
	return d, tx, pending, operationPath, statePath, cleanup
}

func linuxTestFileIdentity(info os.FileInfo) (uint64, uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return uint64(st.Dev), st.Ino
}
