//go:build linux

package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func linuxReceiptRollbackFixture(t *testing.T, present bool) (*LinuxInstaller, nativePaths, Installed, *MaintenanceReceipt) {
	t.Helper()
	h, paths := linuxInstallerFixture(t)
	h.checkUpdaterLock = func(root *os.Root) error {
		info, err := root.Lstat("manager.lock")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("fixture updater lock is not regular")
		}
		return nil
	}
	binary := []byte("candidate manager image for rollback")
	sum := sha256.Sum256(binary)
	expected := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(filepath.Join(paths.Bin, paths.Binary), binary, 0755); err != nil {
		t.Fatal(err)
	}
	if present {
		body, err := json.Marshal(expected)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(paths.State, "installed.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	prior, err := s.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, paths, expected, prior
}

func lockLinuxReceiptFixture(t *testing.T, s *LinuxInstaller) {
	t.Helper()
	if err := s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackRequiresDataLock(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("receipt removal without stopped-manager data lock succeeded")
	}
	if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
		t.Fatalf("receipt changed without lock: %v", err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackRejectsForeignReceipt(t *testing.T) {
	s, _, expected, prior := linuxReceiptRollbackFixture(t, false)
	other, paths, _, _ := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, other)
	if err := other.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("receipt captured by another session was accepted")
	}
	if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
		t.Fatalf("foreign receipt changed: %v", err)
	}
	_ = s.Close()
}

func TestLinuxInstallerRemoveInstalledForRollbackRejectsExpectedMismatch(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	expected.Version = "v1.2.4"
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("mismatched expected receipt was accepted")
	}
	if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
		t.Fatalf("mismatched receipt was removed: %v", err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackHonorsCancellation(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.RemoveInstalledForRollback(ctx, prior, expected); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
		t.Fatalf("cancelled receipt was removed: %v", err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackPreservesReplacement(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	path := filepath.Join(paths.State, "installed.json")
	replacement := []byte(`{"foreign":"replacement"}`)
	if err := os.WriteFile(path+".replacement", replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("replaced receipt was removed")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(replacement) {
		t.Fatalf("replacement was not retained: %q err=%v", got, err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackPreservesSymlink(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	path := filepath.Join(paths.State, "installed.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside", path); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("symlink receipt was removed")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("receipt symlink was not preserved: info=%v err=%v", info, err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackRemovesExactReceipt(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed receipt remains: %v", err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackReportsFenceUncertainty(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, true)
	lockLinuxReceiptFixture(t, s)
	s.hooks.fence = func(context.Context, *os.Root) error { return errors.New("injected sync failure") }
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err == nil {
		t.Fatal("receipt removal sync failure was hidden")
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt removal outcome not reflected: %v", err)
	}
}

func TestLinuxInstallerRemoveInstalledForRollbackIsIdempotentOnCapturedAbsence(t *testing.T) {
	s, paths, expected, prior := linuxReceiptRollbackFixture(t, false)
	lockLinuxReceiptFixture(t, s)
	if err := s.RemoveInstalledForRollback(context.Background(), prior, expected); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installed.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("captured absence was changed: %v", err)
	}
}
