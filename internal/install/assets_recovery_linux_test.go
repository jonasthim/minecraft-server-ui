//go:build linux

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func linuxAssetWriteFixture(t *testing.T, path string, role linuxAssetRole, body string) linuxAssetState {
	t.Helper()
	spec, ok := linuxAssetSpecForRole(role)
	if !ok {
		t.Fatalf("unknown role %q", role)
	}
	if err := os.WriteFile(path, []byte(body), os.FileMode(spec.Mode)); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	state, err := readLinuxAssetAt(context.Background(), root, filepath.Base(path), spec)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReconcileLinuxAssetPublicationScratchCombinations(t *testing.T) {
	const operationID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	t.Run("pending candidate before activation", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		pending := target + ".install-" + operationID
		staged := linuxAssetWriteFixture(t, pending, linuxAssetManager, "candidate")
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, before, staged)
		if err != nil || got != before {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
		if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recognized staged file remains: %v", err)
		}
	})
	t.Run("exchange activated candidate", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		pending := target + ".install-" + operationID
		if err := os.Rename(target, pending); err != nil {
			t.Fatal(err)
		}
		staged := linuxAssetWriteFixture(t, target, linuxAssetManager, "candidate")
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, before, staged)
		if err != nil || got != staged {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
		if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recognized prior inode remains: %v", err)
		}
	})
	t.Run("first install pending candidate", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
		_ = snapshot
		staged := linuxAssetWriteFixture(t, target+".install-"+operationID, linuxAssetManager, "candidate")
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, linuxAssetState{}, staged)
		if err != nil || got.Present {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
	})
	t.Run("completed first install has exact staged public inode", func(t *testing.T) {
		_, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
		staged := linuxAssetWriteFixture(t, target, linuxAssetManager, "candidate")
		unrelated := target + ".install-" + strings.Repeat("b", 64)
		if err := os.WriteFile(unrelated, []byte("foreign scratch"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, linuxAssetState{}, staged)
		if err != nil || got != staged {
			t.Fatalf("reconcile = %+v, %v; want exact staged state %+v", got, err, staged)
		}
		actual := readLinuxAssetFixtureState(t, target, linuxAssetManager)
		if actual != staged {
			t.Fatalf("completed first install state changed: got %+v staged %+v", got, staged)
		}
		if _, err := os.Stat(target + ".install-" + operationID); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("completed first install created or retained pending scratch: %v", err)
		}
		if body, err := os.ReadFile(unrelated); err != nil || string(body) != "foreign scratch" {
			t.Fatalf("unrelated scratch changed: %q, %v", body, err)
		}
	})
	t.Run("completed replacement has exact staged public inode", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		pending := target + ".install-" + operationID
		if err := os.Rename(target, pending); err != nil {
			t.Fatal(err)
		}
		staged := linuxAssetWriteFixture(t, target, linuxAssetManager, "replacement candidate")
		if err := os.Remove(pending); err != nil {
			t.Fatal(err)
		}
		unrelated := target + ".install-" + strings.Repeat("b", 64)
		if err := os.WriteFile(unrelated, []byte("foreign scratch"), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, before, staged)
		if err != nil || got != staged {
			t.Fatalf("reconcile = %+v, %v; want exact staged state %+v", got, err, staged)
		}
		actual := readLinuxAssetFixtureState(t, target, linuxAssetManager)
		if actual != staged {
			t.Fatalf("public state = %+v, want exact staged identity %+v", actual, staged)
		}
		if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("completed replacement retained operation pending file: %v", err)
		}
		if body, err := os.ReadFile(unrelated); err != nil || string(body) != "foreign scratch" {
			t.Fatalf("unrelated scratch changed: %q, %v", body, err)
		}
	})
	t.Run("two absent sides are ambiguous", func(t *testing.T) {
		_, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
		staged := linuxAssetWriteFixture(t, filepath.Join(filepath.Dir(target), "known-stage"), linuxAssetManager, "candidate")
		if _, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, linuxAssetState{}, staged); err == nil {
			t.Fatal("accepted both public and staged paths absent")
		}
	})
	t.Run("same bytes on a foreign pending inode are preserved", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		pending := target + ".install-" + operationID
		if err := os.WriteFile(pending, []byte("candidate"), 0755); err != nil {
			t.Fatal(err)
		}
		foreignStagePath := filepath.Join(filepath.Dir(target), "other-stage")
		staged := linuxAssetWriteFixture(t, foreignStagePath, linuxAssetManager, "candidate")
		if _, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, before, staged); err == nil {
			t.Fatal("accepted a same-bytes foreign pending inode")
		}
		if _, err := os.Stat(pending); err != nil {
			t.Fatalf("foreign pending inode was removed: %v", err)
		}
	})
	t.Run("unrelated scratch is preserved", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		unknown := target + ".install-" + strings.Repeat("b", 64)
		if err := os.WriteFile(unknown, []byte("foreign"), 0600); err != nil {
			t.Fatal(err)
		}
		staged := linuxAssetWriteFixture(t, filepath.Join(filepath.Dir(target), "known-stage"), linuxAssetManager, "candidate")
		got, err := reconcileLinuxAssetEffect(context.Background(), operationID, linuxAssetManager, snapshot.State(), staged)
		if err != nil || got != snapshot.State() {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
		if body, err := os.ReadFile(unknown); err != nil || string(body) != "foreign" {
			t.Fatalf("unrelated scratch changed: %q, %v", body, err)
		}
	})
}

func readLinuxAssetFixtureState(t *testing.T, path string, role linuxAssetRole) linuxAssetState {
	t.Helper()
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	state, err := readLinuxAssetAt(context.Background(), root, filepath.Base(path), mustLinuxAssetSpec(role))
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReconcileLinuxAssetRemovalScratchCombinations(t *testing.T) {
	t.Run("quarantined prior inode is cleaned", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		quarantine := target + ".remove-" + strings.ToLower(strconvHex(before.Device)) + "-" + strings.ToLower(strconvHex(before.Inode))
		if err := os.Rename(target, quarantine); err != nil {
			t.Fatal(err)
		}
		got, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("c", 64), linuxAssetManager, before, linuxAssetState{})
		if err != nil || got.Present {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
		if _, err := os.Stat(quarantine); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("recognized quarantine remains: %v", err)
		}
	})
	t.Run("prior public file is untouched", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		got, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("c", 64), linuxAssetManager, before, linuxAssetState{})
		if err != nil || got != before {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("public prior was removed: %v", err)
		}
	})
	t.Run("already removed is idempotent", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		got, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("c", 64), linuxAssetManager, before, linuxAssetState{})
		if err != nil || got.Present {
			t.Fatalf("reconcile = %+v, %v", got, err)
		}
	})
	t.Run("public prior plus quarantine is ambiguous", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		quarantine := target + ".remove-" + strings.ToLower(strconvHex(before.Device)) + "-" + strings.ToLower(strconvHex(before.Inode))
		if err := os.WriteFile(quarantine, []byte("old asset"), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("c", 64), linuxAssetManager, before, linuxAssetState{}); err == nil {
			t.Fatal("accepted public prior and a same-bytes quarantine")
		}
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("ambiguous quarantine was removed: %v", err)
		}
		if _, err := os.Stat(target); err != nil {
			t.Fatalf("public prior changed: %v", err)
		}
	})
	t.Run("cancelled reconciliation preserves quarantine", func(t *testing.T) {
		snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, true)
		before := snapshot.State()
		quarantine := target + ".remove-" + strings.ToLower(strconvHex(before.Device)) + "-" + strings.ToLower(strconvHex(before.Inode))
		if err := os.Rename(target, quarantine); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := reconcileLinuxAssetEffect(ctx, strings.Repeat("c", 64), linuxAssetManager, before, linuxAssetState{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled reconcile error = %v", err)
		}
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("cancelled reconciliation removed quarantine: %v", err)
		}
	})
}

func TestReconcileLinuxAssetAllowsOnlyAbsentNoEffectWithMissingParent(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	paths := make(map[linuxAssetRole]string)
	for _, spec := range linuxAssetSpecs() {
		paths[spec.Role] = filepath.Join(root, "not-provisioned", string(spec.Role), filepath.Base(spec.Path))
	}
	prior := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: root, uid: uint32(os.Geteuid()), paths: paths}
	t.Cleanup(func() { linuxAssetTestPolicy = prior })
	got, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("d", 64), linuxAssetManager, linuxAssetState{}, linuxAssetState{})
	if err != nil || got != (linuxAssetState{}) {
		t.Fatalf("absent no-effect reconcile = %+v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-provisioned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reconciliation created the missing parent: %v", err)
	}
	staged := linuxAssetState{Present: true, SHA256: strings.Repeat("0", 64), Size: 1, Mode: 0755, Inode: 1}
	if _, err := reconcileLinuxAssetEffect(context.Background(), strings.Repeat("d", 64), linuxAssetManager, linuxAssetState{}, staged); err == nil {
		t.Fatal("missing parent accepted a staged publication")
	}
}

func strconvHex(value uint64) string {
	const digits = "0123456789abcdef"
	if value == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for value != 0 {
		i--
		buf[i] = digits[value&15]
		value >>= 4
	}
	return string(buf[i:])
}
