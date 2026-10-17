//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func linuxAssetFixture(t *testing.T, role linuxAssetRole, present bool) (*linuxAssetSnapshot, string, *os.Root) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	spec, ok := linuxAssetSpecForRole(role)
	if !ok {
		t.Fatalf("unknown fixture role %q", role)
	}
	target := filepath.Join(root, "assets", string(spec.Role), filepath.Base(spec.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string)
	for _, candidate := range linuxAssetSpecs() {
		paths[candidate.Role] = filepath.Join(root, "assets", string(candidate.Role), filepath.Base(candidate.Path))
	}
	prior := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: root, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = prior })
	if present {
		if err := os.WriteFile(target, []byte("old asset"), os.FileMode(spec.Mode)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := inspectLinuxAsset(context.Background(), role)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	opDir := filepath.Join(root, "operation")
	if err := os.Mkdir(opDir, 0700); err != nil {
		t.Fatal(err)
	}
	operation, err := os.OpenRoot(opDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = operation.Close() })
	return snapshot, target, operation
}

func linuxAssetNext(data []byte, mode uint32) linuxAssetState {
	sum := sha256.Sum256(data)
	return linuxAssetState{Present: true, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Mode: mode}
}

func TestLinuxAssetSpecsAreFixedCopiesAndRolesAreExact(t *testing.T) {
	specs := linuxAssetSpecs()
	if len(specs) != 14 {
		t.Fatalf("got %d fixed specs, want 14", len(specs))
	}
	specs[0].Path = "/tmp/attacker"
	if linuxAssetSpecs()[0].Path == "/tmp/attacker" {
		t.Fatal("caller mutation changed fixed asset targets")
	}
	if _, ok := linuxAssetSpecForRole("manager/../other"); ok {
		t.Fatal("accepted a non-exact role")
	}
	for _, spec := range linuxAssetSpecs() {
		if spec.Role == "update_trust" && (spec.Member != "" || spec.Path != "/etc/minecraft-ui/update-trust.json") {
			t.Fatalf("unexpected update trust member or target: %+v", spec)
		}
		if spec.Member == "install.sh" || spec.Member == "empty update-trust.example.json" {
			t.Fatalf("unsafe trust/install member published: %+v", spec)
		}
	}
}

func TestInspectLinuxAssetIsReadOnlyAndCapturesIdentity(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	state := snapshot.State()
	if !state.Present || state.SHA256 != linuxAssetNext([]byte("old asset"), 0755).SHA256 || state.Mode != 0755 || state.Inode == 0 || state.Device == 0 {
		t.Fatalf("unexpected inspected state: %+v", state)
	}
	if _, err := os.Stat(target + ".install-" + strings.Repeat("a", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created an output: %v", err)
	}
}

func TestCaptureLinuxAssetVerifiesAndExclusivelyStoresPreimage(t *testing.T) {
	snapshot, _, operation := linuxAssetFixture(t, linuxAssetManager, true)
	if err := captureLinuxAsset(context.Background(), snapshot, operation); err != nil {
		t.Fatal(err)
	}
	file, err := operation.Open("preimage/manager")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preimage mode/stat = %v, %v", info, err)
	}
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("captured bytes = %q, %v", body, err)
	}
	if err := captureLinuxAsset(context.Background(), snapshot, operation); err == nil {
		t.Fatal("capture overwrote an existing preimage")
	}
}

func TestPublishLinuxAssetActivatesOnlyVerifiedSource(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	sourcePath := filepath.Join(filepath.Dir(target), "source")
	content := []byte("new verified manager")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	next := linuxAssetNext(content, 0755)
	state, err := publishLinuxAsset(context.Background(), strings.Repeat("b", 64), snapshot, source, next)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != string(content) {
		t.Fatalf("published body = %q, %v", body, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0755 || state.Inode == 0 || !state.Present {
		t.Fatalf("published state/info = %+v, %v, %v", state, info, err)
	}
}

func TestPublishLinuxAssetRecordedCallsBeforeRenameWithExactStagedIdentity(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("recorded manager candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "recorded-source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var recorded linuxAssetState
	calls := 0
	actual, err := publishLinuxAssetRecorded(context.Background(), strings.Repeat("a", 64), snapshot, source, linuxAssetNext(content, 0755), func(state linuxAssetState) error {
		calls++
		recorded = state
		body, readErr := os.ReadFile(target)
		if readErr != nil || string(body) != "old asset" {
			return errors.New("record ran after public target changed")
		}
		if !state.Present || state.SHA256 != linuxAssetNext(content, 0755).SHA256 || state.Inode == 0 {
			return errors.New("record did not receive exact staged identity")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || actual != recorded {
		t.Fatalf("record calls/state = %d / %+v, published = %+v", calls, recorded, actual)
	}
}

func TestPublishLinuxAssetRecordedFailureLeavesPriorAndCleansOwnedStage(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "record-failure-source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := publishLinuxAssetRecorded(context.Background(), strings.Repeat("a", 64), snapshot, source, linuxAssetNext(content, 0755), func(linuxAssetState) error {
		return errors.New("injected durable record failure")
	}); err == nil {
		t.Fatal("publication ignored the durable record failure")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("prior target changed on record failure: %q, %v", body, err)
	}
	if _, err := os.Stat(target + ".install-" + strings.Repeat("a", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned staged file remains after record failure: %v", err)
	}
}

func TestPublishLinuxAssetRecordedRechecksCallbackMutations(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "record-mutation-source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := publishLinuxAssetRecorded(context.Background(), strings.Repeat("a", 64), snapshot, source, linuxAssetNext(content, 0755), func(linuxAssetState) error {
		if err := os.Rename(target, target+".original"); err != nil {
			return err
		}
		return os.WriteFile(target, []byte("old asset"), 0755)
	}); err == nil {
		t.Fatal("publication ignored a target replacement made during durable recording")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("foreign replacement changed: %q, %v", body, err)
	}
	if _, err := os.Stat(target + ".install-" + strings.Repeat("a", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned staged file remains after changed target: %v", err)
	}
}

func TestPublishLinuxAssetRecordedPreservesChangedStageAfterCallback(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "stage-mutation-source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	pending := target + ".install-" + strings.Repeat("a", 64)
	if _, err := publishLinuxAssetRecorded(context.Background(), strings.Repeat("a", 64), snapshot, source, linuxAssetNext(content, 0755), func(linuxAssetState) error {
		return os.WriteFile(pending, []byte("changed staged bytes"), 0755)
	}); err == nil {
		t.Fatal("publication ignored a staged content change during durable recording")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("prior target changed: %q, %v", body, err)
	}
	if body, err := os.ReadFile(pending); err != nil || string(body) != "changed staged bytes" {
		t.Fatalf("changed stage was not preserved: %q, %v", body, err)
	}
}

func TestPublishLinuxAssetRejectsHashMismatchWithoutActivation(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	sourcePath := filepath.Join(filepath.Dir(target), "source")
	if err := os.WriteFile(sourcePath, []byte("wrong"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := publishLinuxAsset(context.Background(), strings.Repeat("c", 64), snapshot, source, linuxAssetNext([]byte("expected"), 0755)); err == nil {
		t.Fatal("publication accepted a source hash mismatch")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("target changed after source mismatch: %q, %v", body, err)
	}
}

func TestPublishLinuxAssetFailureBeforeRenameLeavesPriorTarget(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	linuxAssetTestPolicy.beforeRename = func(linuxAssetRole) error { return errors.New("injected before rename") }
	if _, err := publishLinuxAsset(context.Background(), strings.Repeat("e", 64), snapshot, source, linuxAssetNext(content, 0755)); err == nil {
		t.Fatal("publication ignored the injected pre-rename failure")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("prior target changed before rename: %q, %v", body, err)
	}
	if _, err := os.Stat(target + ".install-" + strings.Repeat("e", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned pending file was not cleaned: %v", err)
	}
}

func TestPublishLinuxAssetReportsPostRenameUncertaintyAndActualState(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate after rename")
	sourcePath := filepath.Join(filepath.Dir(target), "source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	linuxAssetTestPolicy.afterRename = func(linuxAssetRole) error { return errors.New("injected after rename") }
	actual, err := publishLinuxAsset(context.Background(), strings.Repeat("f", 64), snapshot, source, linuxAssetNext(content, 0755))
	if err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatalf("post-rename error = %v, want honest uncertainty", err)
	}
	body, readErr := os.ReadFile(target)
	if readErr != nil || string(body) != string(content) || actual.SHA256 != linuxAssetNext(content, 0755).SHA256 || actual.Inode == 0 {
		t.Fatalf("post-rename target/state = %q, %+v, %v", body, actual, readErr)
	}
}

func TestPublishLinuxAssetPreservesForeignPendingAndReplacement(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	content := []byte("candidate")
	sourcePath := filepath.Join(filepath.Dir(target), "source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	const id = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	pending := target + ".install-" + id
	foreign := []byte("foreign")
	if err := os.WriteFile(pending, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := publishLinuxAsset(context.Background(), id, snapshot, source, linuxAssetNext(content, 0755)); err == nil {
		t.Fatal("publication overwrote a foreign pending file")
	}
	body, err := os.ReadFile(pending)
	if err != nil || string(body) != string(foreign) {
		t.Fatalf("foreign pending changed: %q, %v", body, err)
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, pending); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old asset"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := publishLinuxAsset(context.Background(), id, snapshot, source, linuxAssetNext(content, 0755)); err == nil {
		t.Fatal("publication accepted a byte-identical replacement inode")
	}
	body, err = os.ReadFile(target)
	if err != nil || string(body) != "old asset" {
		t.Fatalf("replacement was changed: %q, %v", body, err)
	}
}

func TestInspectLinuxAssetRejectsSymlinkAndHardlink(t *testing.T) {
	_, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
	outside := filepath.Join(filepath.Dir(target), "outside")
	if err := os.WriteFile(outside, []byte("foreign"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectLinuxAsset(context.Background(), linuxAssetManager); err == nil {
		t.Fatal("inspection accepted a symlink target")
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, target); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectLinuxAsset(context.Background(), linuxAssetManager); err == nil {
		t.Fatal("inspection accepted a multiply-linked target")
	}
}

func TestRemoveLinuxAssetIsIdempotentAndRefusesReplacement(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	if err := removeLinuxAsset(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if err := removeLinuxAsset(context.Background(), snapshot); err != nil {
		t.Fatalf("repeated removal failed: %v", err)
	}
	if err := os.WriteFile(target, []byte("replacement"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := removeLinuxAsset(context.Background(), snapshot); err == nil {
		t.Fatal("removal deleted a replacement inode")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "replacement" {
		t.Fatalf("replacement changed: %q, %v", body, err)
	}
}

func TestLinuxAssetMissingParentIsReadOnlyAndCancellationStopsEffects(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	paths := map[linuxAssetRole]string{}
	for _, spec := range linuxAssetSpecs() {
		paths[spec.Role] = filepath.Join(root, "absent", string(spec.Role), filepath.Base(spec.Path))
	}
	prior := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: root, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = prior })
	snapshot, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if snapshot.State() != (linuxAssetState{}) {
		t.Fatalf("missing asset state = %+v", snapshot.State())
	}
	if _, err := os.Stat(filepath.Join(root, "absent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspection created parent: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeLinuxAsset(cancelled, snapshot); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled removal error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths[linuxAssetManager]), 0700); err != nil {
		t.Fatal(err)
	}
	content := []byte("first install")
	sourcePath := filepath.Join(root, "source")
	if err := os.WriteFile(sourcePath, content, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := publishLinuxAsset(context.Background(), strings.Repeat("1", 64), snapshot, source, linuxAssetNext(content, 0755)); err == nil {
		t.Fatal("publication used the stale snapshot after parent provisioning")
	}
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	refreshed, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	defer refreshed.Close()
	if _, err := publishLinuxAsset(context.Background(), strings.Repeat("2", 64), refreshed, source, linuxAssetNext(content, 0755)); err != nil {
		t.Fatalf("publication with reacquired explicit absence failed: %v", err)
	}
}

func TestLinuxAssetRejectsUnsafeModeAndLinks(t *testing.T) {
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
	if err := os.Chmod(target, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectLinuxAsset(context.Background(), linuxAssetManager); err == nil {
		t.Fatal("inspection accepted incorrect role mode")
	}
	if err := os.Chmod(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, target+".link"); err != nil {
		t.Fatal(err)
	}
	if err := removeLinuxAsset(context.Background(), snapshot); err == nil {
		t.Fatal("removal ignored a changed link count")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("unsafe linked asset was removed: %v", err)
	}
}
