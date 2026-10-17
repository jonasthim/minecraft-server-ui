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
	"strings"
	"testing"
	"time"
)

func makeMaintenanceReconcileChallenge(installed Installed) MaintenanceChallenge {
	to := Installed{Format: 1, Version: "v1.2.4", Schema: installed.Schema, SHA256: strings.Repeat("c", 64)}
	return MaintenanceChallenge{Format: 1, OperationID: strings.Repeat("d", 64), Nonce: strings.Repeat("e", 64), Mode: "apply", From: &installed, To: &to, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
}

func writeMaintenanceReconcileProof(t *testing.T, data string, challenge MaintenanceChallenge) {
	t.Helper()
	root := controlDirectory(data)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	challengeBytes, err := json.Marshal(challenge)
	if err != nil {
		t.Fatal(err)
	}
	receipt := MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"maintenance-challenge.json": challengeBytes, "maintenance-drained.json": receiptBytes} {
		if err := os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func fixtureMaintenanceUpdaterLock(root *os.Root) error {
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

func newTerminalMaintenanceReconcileFixture(t *testing.T) (*LinuxInstaller, nativePaths, *LinuxUpdaterSnapshot, MaintenanceChallenge) {
	t.Helper()
	h, paths := linuxInstallerFixture(t)
	h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
	prior, candidate := prepareLinuxTerminalFixture(t, paths, "healthy", "candidate", false)
	_ = prior
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	snapshot, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, paths, snapshot, makeMaintenanceReconcileChallenge(candidate)
}

func TestLinuxInstallerMaintenanceReconcilePreservesOwnProof(t *testing.T) {
	s, paths, snapshot, challenge := newTerminalMaintenanceReconcileFixture(t)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	if err := s.ReconcileUpdaterForMaintenance(context.Background(), snapshot, challenge); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"maintenance-challenge.json", "maintenance-drained.json"} {
		if _, err := os.Stat(filepath.Join(controlDirectory(paths.Data), name)); err != nil {
			t.Fatalf("maintenance proof %s was removed: %v", name, err)
		}
	}
	for _, path := range []string{filepath.Join(paths.State, "activation.json"), filepath.Join(paths.State, "request.json"), filepath.Join(paths.Bin, paths.Binary+".previous"), filepath.Join(paths.Bin, paths.Binary+".next")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("terminal updater file remains %s: %v", path, err)
		}
	}
}

func TestLinuxInstallerMaintenanceReconcileRejectsInvalidProofBeforeCleanup(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, string, MaintenanceChallenge)
	}{
		{"malformed_challenge", func(t *testing.T, data string, c MaintenanceChallenge) {
			writeMaintenanceReconcileProof(t, data, c)
			path := filepath.Join(controlDirectory(data), "maintenance-challenge.json")
			if err := os.WriteFile(path, []byte(`{"format":1,"unknown":true}`), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"mismatched_nonce", func(t *testing.T, data string, c MaintenanceChallenge) {
			c.Nonce = strings.Repeat("f", 64)
			writeMaintenanceReconcileProof(t, data, c)
		}},
		{"wrong_from_image", func(t *testing.T, data string, c MaintenanceChallenge) {
			from := *c.From
			from.Version = "v1.2.2"
			c.From = &from
			writeMaintenanceReconcileProof(t, data, c)
		}},
		{"foreign_extra", func(t *testing.T, data string, c MaintenanceChallenge) {
			writeMaintenanceReconcileProof(t, data, c)
			if err := os.WriteFile(filepath.Join(controlDirectory(data), "foreign.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"pending_receipt", func(t *testing.T, data string, c MaintenanceChallenge) {
			writeMaintenanceReconcileProof(t, data, c)
			if err := os.WriteFile(filepath.Join(controlDirectory(data), "maintenance-drained.pending"), []byte("pending"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, paths, snapshot, challenge := newTerminalMaintenanceReconcileFixture(t)
			tc.mutate(t, paths.Data, challenge)
			if err := s.ReconcileUpdaterForMaintenance(context.Background(), snapshot, challenge); err == nil {
				t.Fatal("invalid maintenance proof was accepted")
			}
			for _, path := range []string{filepath.Join(paths.State, "activation.json"), filepath.Join(paths.Bin, paths.Binary+".previous"), filepath.Join(paths.Bin, paths.Binary+".next")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatalf("cleanup began before proof validation: %s: %v", path, err)
				}
			}
		})
	}
}

func TestLinuxInstallerMaintenanceReconcileRejectsReplacedProofBeforeCleanup(t *testing.T) {
	s, paths, snapshot, challenge := newTerminalMaintenanceReconcileFixture(t)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	challengePath := filepath.Join(controlDirectory(paths.Data), "maintenance-challenge.json")
	baseService := s.hooks.serviceState
	calls := 0
	s.hooks.serviceState = func(ctx context.Context) (linuxServiceState, error) {
		calls++
		if calls == 2 {
			if err := os.WriteFile(challengePath+".replacement", []byte(`{"foreign":"replacement"}`), 0600); err != nil {
				return linuxServiceState{}, err
			}
			if err := os.Rename(challengePath+".replacement", challengePath); err != nil {
				return linuxServiceState{}, err
			}
		}
		return baseService(ctx)
	}
	if err := s.ReconcileUpdaterForMaintenance(context.Background(), snapshot, challenge); err == nil {
		t.Fatal("replaced challenge was accepted")
	}
	for _, path := range []string{filepath.Join(paths.State, "activation.json"), filepath.Join(paths.Bin, paths.Binary+".previous"), filepath.Join(paths.Bin, paths.Binary+".next")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("replacement was detected after cleanup began: %s: %v", path, err)
		}
	}
}

func TestLinuxInstallerOrdinaryReconcileStillRejectsMaintenanceProof(t *testing.T) {
	s, paths, snapshot, challenge := newTerminalMaintenanceReconcileFixture(t)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	if err := s.ReconcileUpdater(context.Background(), snapshot); err == nil {
		t.Fatal("ordinary reconciliation accepted maintenance control entries")
	}
	if _, err := os.Stat(filepath.Join(paths.State, "activation.json")); err != nil {
		t.Fatalf("ordinary reconciliation changed terminal journal: %v", err)
	}
}

func TestLinuxInstallerMaintenanceReconcileAllowsValidatedControlRootTransition(t *testing.T) {
	h, paths := linuxInstallerFixture(t)
	h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
	binary := []byte("installed manager image")
	sum := sha256.Sum256(binary)
	installed := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(filepath.Join(paths.Bin, paths.Binary), binary, 0755); err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.State, "installed.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	snapshot, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.HasTerminal() {
		t.Fatal("expected ordinary fresh snapshot")
	}
	challenge := makeMaintenanceReconcileChallenge(installed)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdaterForMaintenance(context.Background(), snapshot, challenge); err != nil {
		t.Fatalf("validated absent-to-present control root transition failed: %v", err)
	}
	if _, err = os.Stat(filepath.Join(controlDirectory(paths.Data), "maintenance-challenge.json")); err != nil {
		t.Fatalf("nonterminal maintenance proof was not preserved: %v", err)
	}
}

func TestLinuxInstallerMaintenanceReconcileValidatesNonterminalProofBeforeNoop(t *testing.T) {
	h, paths := linuxInstallerFixture(t)
	h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
	binary := []byte("installed manager image")
	sum := sha256.Sum256(binary)
	installed := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(sum[:])}
	if err := os.WriteFile(filepath.Join(paths.Bin, paths.Binary), binary, 0755); err != nil {
		t.Fatal(err)
	}
	receipt, _ := json.Marshal(installed)
	if err := os.WriteFile(filepath.Join(paths.State, "installed.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	snapshot, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	challenge := makeMaintenanceReconcileChallenge(installed)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	if err = os.WriteFile(filepath.Join(controlDirectory(paths.Data), "maintenance-drained.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdaterForMaintenance(context.Background(), snapshot, challenge); err == nil {
		t.Fatal("nonterminal invalid proof was accepted by early no-op")
	}
}
