//go:build linux

package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallerFenceIsReadOnlyAndRejectsEveryOccupiedSlot(t *testing.T) {
	for _, name := range []string{"installation-transaction.json", "installation-transaction.pending", "installation-launch.json", "installation-launch.pending"} {
		for _, kind := range []string{"file", "directory", "dangling"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				dir := t.TempDir()
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer root.Close()
				path := filepath.Join(dir, name)
				switch kind {
				case "file":
					err = os.WriteFile(path, []byte("unparsed installer state"), 0600)
				case "directory":
					err = os.Mkdir(path, 0700)
				case "dangling":
					err = os.Symlink("missing", path)
				}
				if err != nil {
					t.Fatal(err)
				}
				before, err := os.Lstat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := refuseLinuxInstallerState(t.Context(), root); err == nil {
					t.Fatal("occupied installer slot accepted")
				}
				after, err := os.Lstat(path)
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("installer state changed: %v", err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("unexpected filesystem effects: %v %v", entries, err)
				}
			})
		}
	}
}

func TestInstallerFenceAllowsAbsentStateWithoutCreatingLock(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := refuseLinuxInstallerState(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("read-only fence wrote files: %v %v", entries, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := refuseLinuxInstallerState(ctx, root); err == nil {
		t.Fatal("cancelled check accepted")
	}
}

func TestOrdinaryLinuxRecoveryLeavesInstallerTransactionUntouched(t *testing.T) {
	w, _, _, driver := nativeFixture(t)
	file := filepath.Join(w.state.Name(), "installation-transaction.json")
	if err := os.WriteFile(file, []byte("owned by the installer decoder"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	verified := false
	paths := nativePaths{State: w.state.Name(), Bin: w.bin.Name(), Binary: w.binary}
	err = recoverLinuxAt(t.Context(), paths, w.state, w.bin, driver, func(context.Context) error {
		verified = true
		return nil
	})
	if err == nil || verified || driver.stops != 0 || driver.starts != 0 || driver.fences != 0 {
		t.Fatalf("ordinary recovery reached installer state: error=%v verified=%v driver=%+v", err, verified, driver)
	}
	after, err := os.Stat(file)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("installer transaction changed: %v", err)
	}
	if _, err := w.state.Lstat("manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("installer fence did not precede lock creation: %v", err)
	}
}
