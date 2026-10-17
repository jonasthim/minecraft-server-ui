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
	"syscall"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func TestAcquireLinuxInstallerChecksAuthorityBeforePathIO(t *testing.T) {
	called := false
	hooks := linuxInstallerHooks{
		paths:      nativePaths{State: filepath.Join(t.TempDir(), "missing-state"), Bin: filepath.Join(t.TempDir(), "missing-bin"), Binary: "minecraft-ui"},
		authorized: func() error { return errors.New("denied") },
		openProtectedRoot: func(context.Context, string) (*os.Root, error) {
			called = true
			return nil, errors.New("unexpected path access")
		},
	}
	if _, err := acquireLinuxInstaller(context.Background(), hooks); err == nil {
		t.Fatal("unauthorized acquisition succeeded")
	}
	if called {
		t.Fatal("path I/O happened before authority check")
	}
}

func TestLinuxInstallerSnapshotDoesNotAcceptCallerCopy(t *testing.T) {
	session := &LinuxInstaller{}
	snapshot := &LinuxUpdaterSnapshot{owner: session}
	copy := *snapshot
	if _, _, err := session.validateSnapshot(&copy); err == nil {
		t.Fatal("caller-copied snapshot was accepted")
	}
}

func linuxInstallerFixture(t *testing.T) (linuxInstallerHooks, nativePaths) {
	t.Helper()
	base := t.TempDir()
	p := nativePaths{Data: filepath.Join(base, "data"), State: filepath.Join(base, "state"), Bin: filepath.Join(base, "bin"), Binary: "minecraft-ui", Trust: filepath.Join(base, "trust.json"), Updater: filepath.Join(base, "updater")}
	for _, dir := range []string{p.Data, p.State, p.Bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	rootOpener := func(ctx context.Context, path string) (*os.Root, error) { return safepath.OpenRoot(ctx, path) }
	checkFile := func(root *os.Root, name string) error {
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("not regular")
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
			return errors.New("not single-link")
		}
		return nil
	}
	uid := os.Getuid()
	gid := os.Getgid()
	h := linuxInstallerHooks{paths: p, authorized: func() error { return nil }, openProtectedRoot: rootOpener, createStateRoot: func(ctx context.Context, path string) (*os.Root, error) {
		if err := os.Mkdir(path, 0700); err != nil {
			return nil, err
		}
		return rootOpener(ctx, path)
	}, checkProtectedFile: checkFile, acquireLock: launcher.AcquireManager, serviceState: func(context.Context) (linuxServiceState, error) {
		return linuxServiceState{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", MainPID: 0}, nil
	}, openDataRoot: rootOpener, managerIDs: func() (int, int, error) { return uid, gid, nil }, createDataLock: createLinuxManagerLock}
	return h, p
}

func TestLinuxInstallerFreshAcquisitionAllowsMissingBinaryAndState(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	if err := os.Remove(p.State); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(filepath.Join(p.Bin, p.Binary)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("binary unexpectedly present: %v", err)
	}
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := snap.Installed(); present || got != (Installed{}) || snap.HasTerminal() {
		t.Fatalf("unexpected fresh snapshot: %+v present=%v terminal=%v", got, present, snap.HasTerminal())
	}
	clone, err := s.OpenState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer clone.Close()
	a, _ := s.state.Stat(".")
	b, _ := clone.Stat(".")
	if !os.SameFile(a, b) {
		t.Fatal("OpenState returned a different root")
	}
}

func TestLinuxInstallerOpenStateRejectsTransientReplacement(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err = os.Mkdir(replacement, 0700); err != nil {
		t.Fatal(err)
	}
	var calls int
	opener := func(ctx context.Context, path string) (*os.Root, error) {
		calls++
		if path == p.State && calls == 2 {
			return safepath.OpenRoot(ctx, replacement)
		}
		return safepath.OpenRoot(ctx, path)
	}
	s.hooks.openProtectedRoot = opener
	s.maintenance.hooks.openProtectedRoot = opener
	if _, err = s.OpenState(context.Background()); err == nil {
		t.Fatal("transient replacement root was returned")
	}
}

func TestLinuxInstallerRejectsPresentUnsafeBinary(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	if err := os.Symlink("/tmp/foreign", filepath.Join(p.Bin, p.Binary)); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLinuxInstaller(context.Background(), h); err == nil {
		t.Fatal("symlinked manager binary was accepted")
	}
	if _, err := os.Stat(filepath.Join(p.State, "manager.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("updater lock was acquired before binary validation: %v", err)
	}
}

func TestLinuxInstallerStoppedManagerCreatesUsableOwnerLock(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.UnlockManager(); err != nil {
		t.Fatal(err)
	}
	root, err := safepath.OpenRoot(context.Background(), p.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = checkManagerLock(root, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxInstallerRefusesLiveManagerBeforeCreatingDataLock(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	h.serviceState = func(context.Context) (linuxServiceState, error) {
		return linuxServiceState{LoadState: "loaded", ActiveState: "active", SubState: "running", MainPID: 42}, nil
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.LockStoppedManager(context.Background()); err == nil {
		t.Fatal("running manager was accepted")
	}
	if _, err = os.Stat(filepath.Join(p.Data, "manager.lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock was created before stopped check: %v", err)
	}
}

func TestLinuxInstallerPreservesExistingManagerLockOwnerAndMode(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	lockPath := filepath.Join(p.Data, "manager.lock")
	if err := os.WriteFile(lockPath, []byte("held by service account"), 0640); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.UnlockManager(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("existing manager lock mode changed: %o", info.Mode().Perm())
	}
}

func TestLinuxInstallerUsesExistingManagerLockForContention(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	external, err := launcher.AcquireManager(p.Data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		external()
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.LockStoppedManager(context.Background()); err == nil {
		external()
		t.Fatal("contended manager lock was accepted")
	}
	external()
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatalf("released manager lock was not reusable: %v", err)
	}
	if err = s.UnlockManager(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxInstallerLeavesMalformedTerminalStateUntouched(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	path := filepath.Join(p.State, "activation.json")
	original := []byte(`{"format":1,"phase":"healthy","unknown":true}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.SnapshotUpdater(context.Background()); err == nil {
		t.Fatal("malformed terminal state was accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("malformed journal changed: %q", got)
	}
}

func TestLinuxInstallerRejectsUnknownJournalSchemaWithoutMutation(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
	path := filepath.Join(p.State, "activation.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal activationJournal
	if err = json.Unmarshal(body, &journal); err != nil {
		t.Fatal(err)
	}
	journal.Format = 9
	body, err = json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.SnapshotUpdater(context.Background()); err == nil {
		t.Fatal("unknown journal schema was accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, body) {
		t.Fatalf("unknown journal schema changed: err=%v", err)
	}
}

func TestLinuxInstallerReconcilesOnlyMatchingTerminalJournal(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	priorBytes := []byte("prior executable image")
	candidateBytes := []byte("candidate executable image")
	priorHash := sha256.Sum256(priorBytes)
	candidateHash := sha256.Sum256(candidateBytes)
	prior := Installed{Format: 1, Version: "v1.2.2", Schema: 4, SHA256: hex.EncodeToString(priorHash[:])}
	candidate := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(candidateHash[:])}
	if err := os.WriteFile(filepath.Join(p.Bin, p.Binary), candidateBytes, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Bin, p.Binary+".previous"), priorBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Bin, p.Binary+".next"), candidateBytes, 0600); err != nil {
		t.Fatal(err)
	}
	request := ActivationRequest{StageID: strings.Repeat("a", 64), ActorID: 9, RequestedAt: time.Now().UTC().Add(-time.Second)}
	journal := activationJournal{Format: 1, Request: request, Nonce: strings.Repeat("b", 64), Prior: prior, Candidate: candidate, Phase: "healthy"}
	writeJSON := func(path string, v any) {
		t.Helper()
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	writeJSON(filepath.Join(p.State, "activation.json"), journal)
	writeJSON(filepath.Join(p.State, "request.json"), request)
	writeJSON(filepath.Join(p.State, "installed.json"), candidate)
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snap.HasTerminal() {
		t.Fatal("terminal journal was not captured")
	}
	if got, ok := snap.Installed(); !ok || got != candidate {
		t.Fatalf("snapshot image=%+v present=%v", got, ok)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.ReconcileUpdater(cancelled, snap); err == nil {
		t.Fatal("cancelled reconciliation succeeded")
	}
	if _, err = os.Stat(filepath.Join(p.State, "activation.json")); err != nil {
		t.Fatalf("cancelled reconciliation changed journal: %v", err)
	}
	if err = s.ReconcileUpdater(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if err = s.UnlockManager(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(p.State, "activation.json"), filepath.Join(p.State, "request.json"), filepath.Join(p.Bin, p.Binary+".previous"), filepath.Join(p.Bin, p.Binary+".next")} {
		if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("terminal-owned file remains %s: %v", path, err)
		}
	}
	receipt, err := s.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := receipt.Value(); !ok || got != candidate {
		t.Fatalf("receipt after cleanup=%+v present=%v", got, ok)
	}
}

func prepareLinuxTerminalFixture(t *testing.T, p nativePaths, phase, image string, rollback bool) (Installed, Installed) {
	t.Helper()
	priorBytes := []byte("prior executable image")
	candidateBytes := []byte("candidate executable image")
	priorSum := sha256.Sum256(priorBytes)
	candidateSum := sha256.Sum256(candidateBytes)
	prior := Installed{Format: 1, Version: "v1.2.2", Schema: 4, SHA256: hex.EncodeToString(priorSum[:])}
	candidate := Installed{Format: 1, Version: "v1.2.3", Schema: 4, SHA256: hex.EncodeToString(candidateSum[:])}
	current := candidateBytes
	installed := candidate
	if image == "prior" {
		current = priorBytes
		installed = prior
	}
	if err := os.WriteFile(filepath.Join(p.Bin, p.Binary), current, 0755); err != nil {
		t.Fatal(err)
	}
	if phase == "healthy" || phase == "rolled_back" {
		if err := os.WriteFile(filepath.Join(p.Bin, p.Binary+".previous"), priorBytes, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.Bin, p.Binary+".next"), candidateBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	request := ActivationRequest{StageID: strings.Repeat("a", 64), ActorID: 9, RequestedAt: time.Now().UTC().Add(-time.Second)}
	journal := activationJournal{Format: 1, Request: request, Nonce: strings.Repeat("b", 64), Prior: prior, Candidate: candidate, Phase: phase, RollbackAttempted: rollback}
	write := func(name string, v any) {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(p.State, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("activation.json", journal)
	write("installed.json", installed)
	write("request.json", request)
	return prior, candidate
}

func TestLinuxInstallerTerminalPhaseImageRelations(t *testing.T) {
	cases := []struct {
		name, phase, image string
		rollback, want     bool
	}{
		{"healthy_candidate", "healthy", "candidate", false, true},
		{"rolled_back_prior", "rolled_back", "prior", true, true},
		{"aborted_prior", "aborted", "prior", false, true},
		{"healthy_prior_rejected", "healthy", "prior", false, false},
		{"rolled_back_candidate_rejected", "rolled_back", "candidate", true, false},
		{"aborted_rollback_flag_rejected", "aborted", "prior", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, p := linuxInstallerFixture(t)
			prepareLinuxTerminalFixture(t, p, tc.phase, tc.image, tc.rollback)
			s, err := acquireLinuxInstaller(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			snap, err := s.SnapshotUpdater(context.Background())
			if (err == nil) != tc.want {
				t.Fatalf("SnapshotUpdater err=%v wantSuccess=%v", err, tc.want)
			}
			if err == nil && !snap.HasTerminal() {
				t.Fatal("accepted terminal record not marked terminal")
			}
		})
	}
}

func TestLinuxInstallerValidatesResidualChallengeExpiryAtObservationTime(t *testing.T) {
	cases := []struct {
		name       string
		requestAge time.Duration
		expiry     time.Duration
		wrongNonce bool
		want       bool
	}{
		{"producer_nine_minute_challenge", time.Second, 9 * time.Minute, false, true},
		{"late_recovery_challenge_for_old_request", 20 * time.Minute, 3 * time.Minute, false, true},
		{"far_future_expiry", time.Second, 12 * time.Minute, false, false},
		{"nonmatching_nonce", time.Second, 3 * time.Minute, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, p := linuxInstallerFixture(t)
			prior, candidate := prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
			journalBytes, err := os.ReadFile(filepath.Join(p.State, "activation.json"))
			if err != nil {
				t.Fatal(err)
			}
			var journal activationJournal
			if err = json.Unmarshal(journalBytes, &journal); err != nil {
				t.Fatal(err)
			}
			journal.Request.RequestedAt = time.Now().UTC().Add(-tc.requestAge)
			requestBytes, err := json.Marshal(journal.Request)
			if err != nil {
				t.Fatal(err)
			}
			journalBytes, err = json.Marshal(journal)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(p.State, "activation.json"), journalBytes, 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(p.State, "request.json"), requestBytes, 0600); err != nil {
				t.Fatal(err)
			}
			control := controlDirectory(p.Data)
			if err = os.MkdirAll(control, 0700); err != nil {
				t.Fatal(err)
			}
			nonce := journal.Nonce
			if tc.wrongNonce {
				nonce = strings.Repeat("c", 64)
			}
			challenge := Challenge{Format: 1, Nonce: nonce, FromVersion: prior.Version, ToVersion: candidate.Version, ActorID: journal.Request.ActorID, ExpiresAt: time.Now().UTC().Add(tc.expiry)}
			challengeBytes, err := json.Marshal(challenge)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(control, "challenge.json"), challengeBytes, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := acquireLinuxInstaller(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.SnapshotUpdater(context.Background())
			if (err == nil) != tc.want {
				t.Fatalf("SnapshotUpdater err=%v wantSuccess=%v", err, tc.want)
			}
		})
	}
}

func TestLinuxInstallerRejectsCrossSessionSnapshot(t *testing.T) {
	h1, p1 := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p1, "healthy", "candidate", false)
	h2, _ := linuxInstallerFixture(t)
	s1, err := acquireLinuxInstaller(context.Background(), h1)
	if err != nil {
		t.Fatal(err)
	}
	defer s1.Close()
	s2, err := acquireLinuxInstaller(context.Background(), h2)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	snap, err := s1.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s2.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s2.ReconcileUpdater(context.Background(), snap); err == nil {
		t.Fatal("snapshot from another installer session was accepted")
	}
}

func TestLinuxInstallerPreservesReplacedTerminalSource(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.State, "activation.json")
	replacement := []byte(`{"foreign":"replacement"}`)
	tmp := path + ".replacement"
	if err = os.WriteFile(tmp, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdater(context.Background(), snap); err == nil {
		t.Fatal("replacement source was reconciled")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, replacement) {
		t.Fatalf("replacement was not preserved: %q err=%v", got, err)
	}
}

func TestLinuxInstallerReportsPartialDeletionFailure(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
	h.remove = func(root *os.Root, name string) error {
		if name == p.Binary+".next" {
			return errors.New("injected remove failure")
		}
		return root.Remove(name)
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdater(context.Background(), snap); err == nil {
		t.Fatal("partial deletion failure was hidden")
	}
	if _, err = os.Stat(filepath.Join(p.Bin, p.Binary+".previous")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first exact deletion should have occurred: %v", err)
	}
	for _, path := range []string{filepath.Join(p.Bin, p.Binary+".next"), filepath.Join(p.State, "activation.json")} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("remaining source state was lost %s: %v", path, err)
		}
	}
}

func TestLinuxInstallerMidCleanupCancellationReturnsError(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
	ctx, cancel := context.WithCancel(context.Background())
	h.remove = func(root *os.Root, name string) error {
		if err := root.Remove(name); err != nil {
			return err
		}
		if name == p.Binary+".previous" {
			cancel()
		}
		return nil
	}
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdater(ctx, snap); err == nil {
		t.Fatal("mid-cleanup cancellation was hidden")
	}
	if _, err = os.Stat(filepath.Join(p.State, "activation.json")); err != nil {
		t.Fatalf("cancellation unexpectedly removed journal: %v", err)
	}
}

func TestLinuxInstallerFenceFailureIsReported(t *testing.T) {
	h, p := linuxInstallerFixture(t)
	prepareLinuxTerminalFixture(t, p, "healthy", "candidate", false)
	h.fence = func(context.Context, *os.Root) error { return errors.New("injected fence failure") }
	s, err := acquireLinuxInstaller(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snap, err := s.SnapshotUpdater(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.LockStoppedManager(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileUpdater(context.Background(), snap); err == nil {
		t.Fatal("fence failure was hidden")
	}
}

func TestLinuxInstallerRejectsPendingUpdaterMarker(t *testing.T) {
	for _, name := range []string{"activation.pending", "launch.json", "launch.pending", "installed.pending", "installation-receipt.pending"} {
		t.Run(name, func(t *testing.T) {
			h, p := linuxInstallerFixture(t)
			marker := filepath.Join(p.State, name)
			if err := os.WriteFile(marker, []byte("pending"), 0600); err != nil {
				t.Fatal(err)
			}
			s, err := acquireLinuxInstaller(context.Background(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err = s.SnapshotUpdater(context.Background()); err == nil {
				t.Fatal("pending state was accepted")
			}
			got, err := os.ReadFile(marker)
			if err != nil || string(got) != "pending" {
				t.Fatalf("pending marker changed: %q err=%v", got, err)
			}
		})
	}
}
