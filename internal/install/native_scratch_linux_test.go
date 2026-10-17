//go:build linux

package install

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRollbackRefusesUnrecordedPartialAssetScratch(t *testing.T) {
	ctx := context.Background()
	snapshot, target, operation := linuxAssetFixture(t, linuxAssetManager, true)
	defer operation.Close()
	before := snapshot.State()
	id := strings.Repeat("9", 64)
	prior := Image{Version: "v1.2.2", Schema: 18, SHA256: strings.Repeat("a", 64)}
	targetImage := Image{Version: "v1.2.3", Schema: 18, SHA256: strings.Repeat("b", 64)}
	plan := linuxAssetPlan{Role: linuxAssetManager, Before: before, After: linuxAssetNext([]byte("candidate image"), 0755)}
	intent := linuxIntent{Format: 1, ID: id, Mode: modeApply, CreatedAt: time.Now().UTC(), Nonce: strings.Repeat("8", 64), Prior: &prior, Target: &targetImage, Assets: []linuxAssetPlan{plan}}
	tx := transaction{Format: 1, ID: id, Mode: modeApply, Phase: phaseRollingBack, IntentSHA256: strings.Repeat("1", 64), Prior: &prior, Target: &targetImage, FailedStage: "recover"}
	d := &linuxInstallDriver{operation: operation, intent: &intent, tx: &tx, assets: map[linuxAssetRole]*linuxAssetSnapshot{linuxAssetManager: snapshot}, hooks: linuxNativeHooks{testRoot: true}}
	if err := d.writeInitialEffects(ctx); err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(target) + ".install-" + id
	partial, err := snapshot.parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Write([]byte("partial manager copy")); err != nil {
		_ = partial.Close()
		t.Fatal(err)
	}
	if err := partial.Sync(); err != nil {
		_ = partial.Close()
		t.Fatal(err)
	}
	if err := partial.Close(); err != nil {
		t.Fatal(err)
	}

	if err := d.rollbackAssets(ctx, tx, intent); err == nil {
		t.Fatal("rollback accepted unrecorded partial scratch")
	}
	if _, err := d.assets[linuxAssetManager].parent.Lstat(name); err != nil {
		t.Fatalf("rollback removed unowned scratch: %v", err)
	}
}
