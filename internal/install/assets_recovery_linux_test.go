//go:build linux

package install

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

func TestAnonymousAssetStagePreLinkInterruptionIsRecoverable(t *testing.T) {
	ctx := context.Background()
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
	defer snapshot.Close()
	before := snapshot.State()
	id := strings.Repeat("e", 64)
	source, err := os.CreateTemp(t.TempDir(), "candidate-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Write([]byte("signed candidate binary")); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	next := linuxAssetNext([]byte("signed candidate binary"), 0755)
	var staged linuxAssetState
	oldBeforeLink := linuxAssetTestPolicy.beforeLink
	defer func() { linuxAssetTestPolicy.beforeLink = oldBeforeLink }()
	linuxAssetTestPolicy.beforeLink = func(role linuxAssetRole) error { return errors.New("simulated worker death before link") }
	_, publishErr := publishLinuxAssetRecorded(ctx, id, snapshot, source, next, func(state linuxAssetState) error { staged = state; return nil })
	_ = source.Close()
	if publishErr == nil || !staged.Present || staged.Inode == 0 {
		t.Fatalf("publish stage = %+v, %v", staged, publishErr)
	}
	if _, err := os.Lstat(target + ".install-" + id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pre-link stage escaped into namespace: %v", err)
	}
	current, err := inspectLinuxAsset(ctx, linuxAssetManager)
	if err != nil {
		t.Fatal(err)
	}
	if current.State() != before {
		_ = current.Close()
		t.Fatal("public asset changed before link")
	}
	_ = current.Close()
	got, err := reconcileLinuxAssetEffectWithProtocol(ctx, id, linuxAssetManager, before, staged, linuxAssetStagingAnonymousV1)
	if err != nil || got != before {
		t.Fatalf("anonymous pre-link recovery = %+v, %v", got, err)
	}
	if _, err := reconcileLinuxAssetEffect(ctx, id, linuxAssetManager, before, staged); err == nil {
		t.Fatal("legacy named-stage effect accepted missing scratch for absent preimage")
	}
}

func TestAnonymousAssetCleanupPreservesMutatedSameInodeScratch(t *testing.T) {
	ctx := context.Background()
	snapshot, target, _ := linuxAssetFixture(t, linuxAssetManager, false)
	defer snapshot.Close()
	id := strings.Repeat("f", 64)
	body := []byte("expected candidate")
	source, err := os.CreateTemp(t.TempDir(), "candidate-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Write(body); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Base(target) + ".install-" + id
	linuxAssetTestPolicy.beforeRename = func(role linuxAssetRole) error {
		file, err := snapshot.parent.OpenFile(pending, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		_, writeErr := file.Write([]byte("mutated same inode"))
		return errors.Join(writeErr, file.Close(), errors.New("injected pre-rename failure"))
	}
	defer func() { linuxAssetTestPolicy.beforeRename = nil }()
	_, err = publishLinuxAssetRecorded(ctx, id, snapshot, source, linuxAssetNext(body, 0755), func(linuxAssetState) error { return nil })
	_ = source.Close()
	if err == nil {
		t.Fatal("publication succeeded after staged bytes changed")
	}
	file, err := snapshot.parent.Open(pending)
	if err != nil {
		t.Fatalf("cleanup removed changed same-inode scratch: %v", err)
	}
	actual, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(actual) != "mutated same inode" {
		t.Fatalf("preserved scratch=%q err=%v", actual, errors.Join(readErr, closeErr))
	}
}

func TestAnonymousAssetStageSIGKILLBeforeLinkLeavesNoNamedScratch(t *testing.T) {
	const childMarker = "MINECRAFT_UI_ANONYMOUS_STAGE_KILL_CHILD"
	if os.Getenv(childMarker) == "1" {
		linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{
			root: os.Getenv(childMarker + "_ROOT"), uid: uint32(os.Geteuid()),
			paths: map[linuxAssetRole]string{linuxAssetManager: os.Getenv(childMarker + "_TARGET")},
			beforeLink: func(linuxAssetRole) error {
				if err := os.WriteFile(os.Getenv(childMarker+"_READY"), []byte("ready"), 0600); err != nil {
					return err
				}
				select {}
			},
		}
		snapshot, err := inspectLinuxAsset(context.Background(), linuxAssetManager)
		if err != nil {
			t.Fatal(err)
		}
		defer snapshot.Close()
		source, err := os.Open(os.Getenv(childMarker + "_SOURCE"))
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		body, err := os.ReadFile(os.Getenv(childMarker + "_SOURCE"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = publishLinuxAssetRecorded(context.Background(), strings.Repeat("7", 64), snapshot, source, linuxAssetNext(body, 0755), func(staged linuxAssetState) error {
			encoded, err := json.Marshal(staged)
			if err != nil {
				return err
			}
			file, err := os.OpenFile(os.Getenv(childMarker+"_EFFECT"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write(encoded)
			return errors.Join(writeErr, file.Sync(), file.Close())
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("child unexpectedly completed before parent kill")
	}

	root := t.TempDir()
	target := filepath.Join(root, "assets", "manager", "minecraft-ui")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "candidate")
	if err := os.WriteFile(source, []byte("candidate binary before link"), 0600); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "ready")
	effectPath := filepath.Join(root, "effect.json")
	command := exec.Command(os.Args[0], "-test.run=^TestAnonymousAssetStageSIGKILLBeforeLinkLeavesNoNamedScratch$")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, childMarker+"=") && !strings.HasPrefix(entry, childMarker+"_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, childMarker+"=1", childMarker+"_ROOT="+root, childMarker+"_TARGET="+target, childMarker+"_SOURCE="+source, childMarker+"_READY="+ready, childMarker+"_EFFECT="+effectPath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatal("child did not reach the anonymous-stage pre-link checkpoint")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok {
		t.Fatalf("child termination = %v, want signal SIGKILL", waitErr)
	}
	waitStatus, statusOK := exitErr.Sys().(syscall.WaitStatus)
	if !statusOK || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("child termination = %v, want signal SIGKILL", waitErr)
	}
	if _, err := os.Lstat(target + ".install-" + strings.Repeat("7", 64)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SIGKILL left named pre-link scratch: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("SIGKILL changed public asset: %v", err)
	}
	effectBytes, err := os.ReadFile(effectPath)
	if err != nil {
		t.Fatalf("pre-link effect was not durably captured: %v", err)
	}
	var staged linuxAssetState
	if err := decodeLinuxStrictJSON(effectBytes, 4096, &staged); err != nil {
		t.Fatal(err)
	}
	prior := linuxAssetTestPolicy
	linuxAssetTestPolicy = &linuxAssetTestPolicyConfig{root: root, uid: uint32(os.Geteuid()), paths: map[linuxAssetRole]string{linuxAssetManager: target}}
	defer func() { linuxAssetTestPolicy = prior }()
	if got, err := reconcileLinuxAssetEffectWithProtocol(context.Background(), strings.Repeat("7", 64), linuxAssetManager, linuxAssetState{}, staged, linuxAssetStagingAnonymousV1); err != nil || got != (linuxAssetState{}) {
		t.Fatalf("fresh-process pre-link reconciliation = %+v, %v", got, err)
	}
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
