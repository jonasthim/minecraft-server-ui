//go:build linux

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTerminalControlFixture(t *testing.T, paths nativePaths) {
	t.Helper()
	control := controlDirectory(paths.Data)
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	prior, candidate := prepareLinuxTerminalFixture(t, paths, "healthy", "candidate", false)
	journalBytes, err := os.ReadFile(filepath.Join(paths.State, "activation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal activationJournal
	if err := strictJSON(journalBytes, &journal); err != nil {
		t.Fatal(err)
	}
	challenge := Challenge{Format: 1, Nonce: journal.Nonce, FromVersion: prior.Version, ToVersion: candidate.Version, ActorID: journal.Request.ActorID, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	drained := DrainReceipt{Format: 1, Nonce: journal.Nonce, Version: prior.Version, Schema: prior.Schema, ExecutableSHA256: prior.SHA256, Drained: true, CreatedAt: time.Now().UTC()}
	request := CandidateProbeRequest{Format: 1, Nonce: journal.Nonce, Version: candidate.Version, Schema: candidate.Schema, SHA256: candidate.SHA256}
	for name, value := range map[string]any{"challenge.json": challenge, "drained.json": drained, "request.json": request} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(control, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func newControlPreparationFixture(t *testing.T, active bool) (*LinuxInstaller, nativePaths, *LinuxUpdaterSnapshot) {
	t.Helper()
	h, paths := linuxInstallerFixture(t)
	h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
	if active {
		h.serviceState = func(context.Context) (linuxServiceState, error) {
			return linuxServiceState{LoadState: "loaded", ActiveState: "active", SubState: "running", MainPID: 123}, nil
		}
	}
	writeTerminalControlFixture(t, paths)
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	snapshot, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, paths, snapshot
}

func TestPrepareMaintenanceControlRemovesOnlyCapturedTerminalControls(t *testing.T) {
	s, paths, snapshot := newControlPreparationFixture(t, true)
	stateNames := []string{"activation.json", "request.json", "installed.json"}
	assetNames := []string{paths.Binary, paths.Binary + ".previous", paths.Binary + ".next"}
	before := map[string]os.FileInfo{}
	for _, name := range append(stateNames, assetNames...) {
		rootPath := paths.State
		if strings.HasSuffix(name, ".previous") || strings.HasSuffix(name, ".next") || name == paths.Binary {
			rootPath = paths.Bin
		}
		info, err := os.Stat(filepath.Join(rootPath, name))
		if err != nil {
			t.Fatal(err)
		}
		before[filepath.Join(rootPath, name)] = info
	}
	fresh, err := s.PrepareMaintenanceControl(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == snapshot || !fresh.HasTerminal() {
		t.Fatal("terminal controls were not followed by a fresh terminal snapshot")
	}
	for _, name := range []string{"challenge.json", "drained.json", "request.json"} {
		if _, err := os.Lstat(filepath.Join(controlDirectory(paths.Data), name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("captured control %s remains: %v", name, err)
		}
	}
	for path, old := range before {
		current, err := os.Stat(path)
		if err != nil || !os.SameFile(old, current) {
			t.Errorf("ordinary updater state changed at %s: %v", path, err)
		}
	}
	challenge := makeMaintenanceReconcileChallenge(fresh.installed)
	writeMaintenanceReconcileProof(t, paths.Data, challenge)
	s.hooks.serviceState = func(context.Context) (linuxServiceState, error) {
		return linuxServiceState{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", MainPID: 0}, nil
	}
	if err := s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileUpdaterForMaintenance(context.Background(), fresh, challenge); err != nil {
		t.Fatalf("fresh snapshot did not reconcile after manager stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.State, "activation.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("terminal updater journal was not reconciled: %v", err)
	}
}

func TestPrepareMaintenanceControlRejectsForeignPendingAndReplacedControls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, nativePaths)
	}{
		{name: "foreign extra", mutate: func(t *testing.T, p nativePaths) {
			if err := os.WriteFile(filepath.Join(controlDirectory(p.Data), "foreign.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pending control", mutate: func(t *testing.T, p nativePaths) {
			if err := os.WriteFile(filepath.Join(controlDirectory(p.Data), "challenge.pending"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "same bytes new inode", mutate: func(t *testing.T, p nativePaths) {
			path := filepath.Join(controlDirectory(p.Data), "challenge.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, paths, snapshot := newControlPreparationFixture(t, false)
			tc.mutate(t, paths)
			if _, err := s.PrepareMaintenanceControl(context.Background(), snapshot); err == nil {
				t.Fatal("accepted changed control set or file")
			}
			if _, err := os.Stat(filepath.Join(paths.State, "activation.json")); err != nil {
				t.Fatalf("updater journal changed before refusal: %v", err)
			}
			if _, err := os.Stat(filepath.Join(paths.Bin, paths.Binary+".previous")); err != nil {
				t.Fatalf("scratch image changed before refusal: %v", err)
			}
		})
	}
}

func TestPrepareMaintenanceControlRejectsJournalReplacementBeforeDeletion(t *testing.T) {
	s, paths, snapshot := newControlPreparationFixture(t, false)
	path := filepath.Join(paths.State, "activation.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareMaintenanceControl(context.Background(), snapshot); err == nil {
		t.Fatal("accepted replacement activation journal")
	}
	if _, err := os.Stat(filepath.Join(controlDirectory(paths.Data), "challenge.json")); err != nil {
		t.Fatalf("control removed despite journal replacement: %v", err)
	}
}

func TestPrepareMaintenanceControlRejectsPreviouslyAbsentUpdaterArtifacts(t *testing.T) {
	for _, name := range []string{"request.json", "minecraft-ui.previous", "minecraft-ui.next"} {
		t.Run(name, func(t *testing.T) {
			h, paths := linuxInstallerFixture(t)
			h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
			writeTerminalControlFixture(t, paths)
			artifact := name
			root := paths.State
			if strings.HasSuffix(name, ".previous") || strings.HasSuffix(name, ".next") {
				root = paths.Bin
			}
			if err := os.Remove(filepath.Join(root, artifact)); err != nil {
				t.Fatal(err)
			}
			s, err := acquireLinuxInstaller(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			snapshot, err := s.SnapshotUpdater(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if name == "request.json" && snapshot.request != nil || name == "minecraft-ui.previous" && snapshot.previous != nil || name == "minecraft-ui.next" && snapshot.next != nil {
				t.Fatal("fixture artifact was not absent at capture")
			}
			if err := os.WriteFile(filepath.Join(root, artifact), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.PrepareMaintenanceControl(context.Background(), snapshot); err == nil {
				t.Fatal("accepted artifact that appeared after snapshot")
			}
			if _, err := os.Stat(filepath.Join(controlDirectory(paths.Data), "challenge.json")); err != nil {
				t.Fatalf("control changed before refusing appeared artifact: %v", err)
			}
		})
	}
}

func TestPrepareMaintenanceControlRecoversFromPartialDeletion(t *testing.T) {
	s, paths, snapshot := newControlPreparationFixture(t, false)
	removeCalls := 0
	s.hooks.remove = func(root *os.Root, name string) error {
		removeCalls++
		if name == "drained.json" {
			return errors.New("injected stop after first deletion")
		}
		return root.Remove(name)
	}
	if _, err := s.PrepareMaintenanceControl(context.Background(), snapshot); err == nil {
		t.Fatal("injected partial cleanup unexpectedly succeeded")
	}
	if removeCalls != 2 {
		t.Fatalf("remove calls = %d, want 2", removeCalls)
	}
	s.hooks.remove = nil
	resumed, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatalf("snapshot after partial cleanup: %v", err)
	}
	fresh, err := s.PrepareMaintenanceControl(context.Background(), resumed)
	if err != nil || fresh == resumed || !fresh.HasTerminal() {
		t.Fatalf("resume partial cleanup = (%p, %v), want fresh terminal snapshot", fresh, err)
	}
	for _, name := range []string{"challenge.json", "drained.json", "request.json"} {
		if _, err := os.Lstat(filepath.Join(controlDirectory(paths.Data), name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("control %s remains after resumed cleanup: %v", name, err)
		}
	}
}

func TestPrepareMaintenanceControlPreservesConcurrentForeignEntry(t *testing.T) {
	s, paths, snapshot := newControlPreparationFixture(t, false)
	s.hooks.remove = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(controlDirectory(paths.Data), "foreign.json"), []byte(`{}`), 0600)
	}
	if _, err := s.PrepareMaintenanceControl(context.Background(), snapshot); err == nil {
		t.Fatal("concurrent foreign control entry did not fence cleanup")
	}
	if _, err := os.Stat(filepath.Join(controlDirectory(paths.Data), "foreign.json")); err != nil {
		t.Fatalf("foreign entry was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.State, "activation.json")); err != nil {
		t.Fatalf("updater journal changed after fenced cleanup: %v", err)
	}
}

func TestPrepareMaintenanceControlNonterminalIsNoop(t *testing.T) {
	h, _ := linuxInstallerFixture(t)
	h.checkUpdaterLock = fixtureMaintenanceUpdaterLock
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PrepareMaintenanceControl(context.Background(), snapshot)
	if err != nil || got != snapshot {
		t.Fatalf("nonterminal prepare = (%p, %v), want same snapshot", got, err)
	}
}
