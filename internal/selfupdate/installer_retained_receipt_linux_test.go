//go:build linux

package selfupdate

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
	"testing"
)

func retainedReceiptRollbackFixture(t *testing.T) (*LinuxInstaller, nativePaths, Installed, Installed, *MaintenanceReceipt) {
	t.Helper()
	s, paths, expectedCurrent, captured := linuxReceiptRollbackFixture(t, true)
	if err := os.Remove(filepath.Join(paths.Bin, paths.Binary)); err != nil {
		t.Fatal(err)
	}
	retained := Installed{Format: 1, Version: "v1.2.2", Schema: expectedCurrent.Schema, SHA256: strings.Repeat("b", 64)}
	lockLinuxReceiptFixture(t, s)
	return s, paths, expectedCurrent, retained, captured
}

func TestLinuxInstallerRestoreRetainedReceiptAfterAbsentManagerRollback(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()

	if err := s.CompareAndSwapInstalled(context.Background(), captured, retained); err == nil || !strings.Contains(err.Error(), "canonical manager executable") {
		t.Fatalf("ordinary receipt CAS did not specifically reject the missing manager executable: %v", err)
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err != nil {
		t.Fatalf("restore retained receipt after failed reinstall: %v", err)
	}
	current, err := s.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := current.Value(); !present || got != retained {
		t.Fatalf("retained receipt = %+v present=%v, want %+v", got, present, retained)
	}
	wantBytes, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(paths.State, "installed.json"))
	if err != nil || !bytes.Equal(gotBytes, wantBytes) {
		t.Fatalf("retained receipt bytes = %q, want %q, err=%v", gotBytes, wantBytes, err)
	}
	info, err := os.Stat(filepath.Join(paths.State, "installed.json"))
	if err != nil {
		t.Fatal(err)
	}
	idempotent, err := s.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), idempotent, expectedCurrent, retained); err != nil {
		t.Fatalf("idempotent retained receipt restore: %v", err)
	}
	after, err := os.Stat(filepath.Join(paths.State, "installed.json"))
	if err != nil || !os.SameFile(info, after) {
		t.Fatalf("idempotent restore republished receipt: before=%v after=%v err=%v", info, after, err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptRequiresStoppedManagerAndExactExpectation(t *testing.T) {
	t.Run("wrong expected receipt", func(t *testing.T) {
		s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
		defer s.Close()
		expectedCurrent.Version = "v1.2.4"
		if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
			t.Fatal("mismatched expected current receipt was accepted")
		}
		if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
			t.Fatalf("receipt changed after mismatch: %v", err)
		}
	})
	t.Run("active manager", func(t *testing.T) {
		s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
		defer s.Close()
		s.hooks.serviceState = func(context.Context) (linuxServiceState, error) {
			return linuxServiceState{LoadState: "loaded", ActiveState: "active", SubState: "running", MainPID: 12}, nil
		}
		if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
			t.Fatal("active manager was accepted")
		}
		if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
			t.Fatalf("receipt changed while manager active: %v", err)
		}
	})
}

func TestLinuxInstallerRestoreRetainedReceiptRequiresLiveLocks(t *testing.T) {
	t.Run("data lock", func(t *testing.T) {
		h, paths := linuxInstallerFixture(t)
		binary := []byte("candidate manager image for rollback")
		sum := sha256.Sum256(binary)
		current := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(sum[:])}
		if err := os.WriteFile(filepath.Join(paths.Bin, paths.Binary), binary, 0755); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(current)
		if err := os.WriteFile(filepath.Join(paths.State, "installed.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		s, err := acquireLinuxInstaller(context.Background(), h)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		captured, err := s.Receipt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(paths.Bin, paths.Binary)); err != nil {
			t.Fatal(err)
		}
		retained := Installed{Format: 1, Version: "v1.2.2", Schema: current.Schema, SHA256: strings.Repeat("b", 64)}
		if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, current, retained); err == nil {
			t.Fatal("receipt restore without data lock succeeded")
		}
	})
	t.Run("updater lock", func(t *testing.T) {
		s, paths, current, retained, captured := retainedReceiptRollbackFixture(t)
		defer s.Close()
		release := s.maintenance.release
		s.maintenance.release = nil
		if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, current, retained); err == nil {
			t.Fatal("receipt restore without a held updater lock succeeded")
		}
		s.maintenance.release = release
		if _, err := os.Stat(filepath.Join(paths.State, "installed.json")); err != nil {
			t.Fatalf("receipt changed without updater lock: %v", err)
		}
	})
}

func TestLinuxInstallerRestoreRetainedReceiptPreservesRecreatedBinary(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	binaryPath := filepath.Join(paths.Bin, paths.Binary)
	s.maintenance.hooks.beforePublish = func(context.Context, *os.Root) error {
		return os.WriteFile(binaryPath, []byte("foreign executable"), 0755)
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
		t.Fatal("recreated canonical manager binary was accepted")
	}
	if got, err := os.ReadFile(binaryPath); err != nil || string(got) != "foreign executable" {
		t.Fatalf("recreated binary was changed: %q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installation-receipt.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned pending receipt was not cleaned after binary recreation: %v", err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptPreservesForeignReceiptReplacement(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	path := filepath.Join(paths.State, "installed.json")
	replacement := []byte(`{"foreign":"receipt"}`)
	if err := os.WriteFile(path+".replacement", replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".replacement", path); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
		t.Fatal("foreign receipt replacement was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("foreign receipt replacement was not preserved: %q err=%v", got, err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptRejectsAnotherSessionsCapture(t *testing.T) {
	s, _, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	other, _, _, _, _ := retainedReceiptRollbackFixture(t)
	defer other.Close()
	if err := other.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
		t.Fatal("receipt captured by another installer session was accepted")
	}
}

func TestLinuxInstallerRestoreRetainedReceiptPreservesForeignPendingReplacement(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	path := filepath.Join(paths.State, "installation-receipt.pending")
	replacement := []byte(`{"foreign":"pending"}`)
	s.maintenance.hooks.beforePublish = func(_ context.Context, root *os.Root) error {
		if err := root.Remove("installation-receipt.pending"); err != nil {
			return err
		}
		return os.WriteFile(path, replacement, 0600)
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
		t.Fatal("foreign pending replacement was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("foreign pending replacement was not preserved: %q err=%v", got, err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptCleansZeroWriteCancellation(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	s.maintenance.hooks.writePending = func(context.Context, *os.File, []byte) error {
		return context.Canceled
	}
	err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("zero-write cancellation error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installation-receipt.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned zero-byte pending receipt remains: %v", err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptCleansPartialWriteFailure(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	writeErr := errors.New("injected partial receipt write")
	s.maintenance.hooks.writePending = func(_ context.Context, file *os.File, body []byte) error {
		_, err := file.Write(body[:len(body)/2])
		return errors.Join(writeErr, err)
	}
	err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained)
	if !errors.Is(err, writeErr) {
		t.Fatalf("partial write error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installation-receipt.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned partial pending receipt remains: %v", err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptPreservesChangedOwnedPendingBytes(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	path := filepath.Join(paths.State, "installation-receipt.pending")
	body, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	replacement := bytes.Repeat([]byte("x"), len(body))
	s.maintenance.hooks.beforePublish = func(_ context.Context, root *os.Root) error {
		file, err := root.OpenFile("installation-receipt.pending", os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		_, writeErr := file.WriteAt(replacement, 0)
		truncateErr := file.Truncate(int64(len(replacement)))
		return errors.Join(writeErr, truncateErr, file.Close())
	}
	if err := s.RestoreRetainedInstalledForRollback(context.Background(), captured, expectedCurrent, retained); err == nil {
		t.Fatal("same-inode pending-byte mutation was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("changed same-inode pending bytes were not preserved: %q err=%v", got, err)
	}
}

func TestLinuxInstallerRestoreRetainedReceiptHonorsCancellationAndCleansOwnedPending(t *testing.T) {
	s, paths, expectedCurrent, retained, captured := retainedReceiptRollbackFixture(t)
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	s.maintenance.hooks.beforePublish = func(context.Context, *os.Root) error {
		cancel()
		return nil
	}
	if err := s.RestoreRetainedInstalledForRollback(ctx, captured, expectedCurrent, retained); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled receipt restoration error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(paths.State, "installation-receipt.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned pending receipt was not cleaned: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(paths.State, "installed.json"))
	if err != nil || len(body) == 0 {
		t.Fatalf("expected receipt changed after cancellation: %v", err)
	}
}
