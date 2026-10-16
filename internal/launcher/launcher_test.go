package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestRejectUnsafeLaunch(t *testing.T) {
	for _, id := range []string{"../escape", "-a", "A", "a/b"} {
		if _, err := Paths(t.TempDir(), id); err == nil {
			t.Errorf("accepted id %q", id)
		}
	}
	p, err := Paths(t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p.State, filepath.Join("test", "state")) {
		t.Fatal(p)
	}
	if err := ValidateSpec(domain.LaunchSpec{Executable: "java", WorkingDir: p.Server}, p); err == nil {
		t.Fatal("relative executable accepted")
	}
}

func TestIdentityRejectsReuseAndMissingBoot(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id, err := CaptureIdentity("test", os.Getpid(), domain.LaunchSpec{Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyIdentity(id); err != nil {
		t.Fatal(err)
	}
	id.StartIdentity += "reused"
	if err := VerifyIdentity(id); err == nil {
		t.Fatal("reused pid accepted")
	}
	id.StartIdentity = ""
	if err := VerifyIdentity(id); err == nil {
		t.Fatal("empty identity accepted")
	}
}

func TestLeaseExpiresAttemptsSaveOnAndRejectsExtension(t *testing.T) {
	var commands []string
	lease := newLeaseGuard(func(s string) error { commands = append(commands, s); return nil })
	now := time.Now()
	if err := lease.arm("lease", now.Add(6*time.Minute), now); err == nil {
		t.Fatal("unbounded lease")
	}
	if err := lease.arm("lease", now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}
	if err := lease.arm("other", now.Add(time.Second), now); err == nil {
		t.Fatal("overlapping lease")
	}
	if err := lease.expire(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0] != "save-off" || commands[1] != "save-on" {
		t.Fatal(commands)
	}
}

func TestLeaseRetriesFailedSaveOn(t *testing.T) {
	calls := 0
	lease := newLeaseGuard(func(command string) error {
		if command == "save-off" {
			return nil
		}
		calls++
		if calls == 1 {
			return errors.New("blocked")
		}
		return nil
	})
	now := time.Now()
	if err := lease.arm("lease", now.Add(time.Second), now); err != nil {
		t.Fatal(err)
	}
	if err := lease.expire(now.Add(2 * time.Second)); err == nil {
		t.Fatal("hidden save-on failure")
	}
	if err := lease.expire(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestManagerLockRejectsDuplicateAndReleases(t *testing.T) {
	root := t.TempDir()
	release, err := AcquireManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := AcquireManager(root); err == nil {
		duplicate()
		t.Fatal("duplicate manager acquired data directory")
	}
	release()
	next, err := AcquireManager(root)
	if err != nil {
		t.Fatal("manager lock not released", err)
	}
	next()
}
func TestAdmissionLockSurvivesInstanceRenameAndMissingRoot(t *testing.T) {
	root := t.TempDir()
	release, err := AcquireAdmission(root, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = os.Stat(filepath.Join(root, "instances", "test")); !os.IsNotExist(err) {
		t.Fatal("guard created a ghost instance")
	}
	if err = os.MkdirAll(filepath.Join(root, "instances", "test"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(root, "instances", "test"), filepath.Join(root, "instances", ".deleting-test")); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := AcquireAdmission(root, "test"); err == nil {
		duplicate()
		t.Fatal("renaming instance replaced lock identity")
	}
}

func TestPrivateLocksRejectSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "victim")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "manager.lock")); err != nil {
		t.Skip("native symlink creation privilege unavailable", err)
	}
	if release, err := AcquireManager(root); err == nil {
		release()
		t.Fatal("followed symlink lock")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatal("symlink target modified", err)
	}
}

func TestCommandRejectsIndirectLifecycleExecution(t *testing.T) {
	for _, command := range []string{"execute run save-off", "minecraft:execute run minecraft:stop", "function pack:stop", "schedule function pack:save-off 1t", "return run stop", "debug function pack:unsafe", "/MiNeCrAfT:EXECUTE run SAVE-OFF", "minecraft:debug minecraft:function pack:unsafe"} {
		if err := ValidateCommand(command); err == nil {
			t.Errorf("indirect lifecycle command accepted: %q", command)
		}
	}
	for _, command := range []string{"list", "say Hello", "save-all flush", "minecraft:save-on"} {
		if err := ValidateCommand(command); err != nil {
			t.Errorf("ordinary transport command rejected: %q: %v", command, err)
		}
	}
}
