package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

type maintenanceFixture struct {
	t         *testing.T
	paths     nativePaths
	hooks     maintenanceHooks
	binary    []byte
	installed Installed
}

func newMaintenanceFixture(t *testing.T) maintenanceFixture {
	t.Helper()
	base := t.TempDir()
	statePath := filepath.Join(base, "state")
	binPath := filepath.Join(base, "bin")
	dataPath := filepath.Join(base, "data")
	for _, path := range []string{statePath, binPath, dataPath} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := []byte("verified manager executable")
	if err := os.WriteFile(filepath.Join(binPath, "minecraft-ui"), binary, 0700); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(binary)
	paths := nativePaths{Data: dataPath, State: statePath, Bin: binPath, Binary: "minecraft-ui"}
	fixture := maintenanceFixture{t: t, paths: paths, binary: binary, installed: Installed{Format: 1, Version: "v1.0.0", Schema: 18, SHA256: hex.EncodeToString(h[:])}}
	fixture.hooks = maintenanceHooks{
		paths:             paths,
		authorized:        func() error { return nil },
		openProtectedRoot: func(_ context.Context, path string) (*os.Root, error) { return os.OpenRoot(path) },
		openSafeRoot:      openFixtureSafeRoot,
		checkProtectedFile: func(root *os.Root, name string) error {
			info, err := root.Lstat(name)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("fixture protected file invalid")
			}
			return nil
		},
		acquireLock: func(path string) (func(), error) { return launcherAcquireManager(path) },
		fence:       func(context.Context, *os.Root) error { return nil },
	}
	return fixture
}

func openFixtureSafeRoot(ctx context.Context, path string) (*os.Root, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	missing := false
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
		} else if err != nil {
			return nil, err
		} else if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("fixture safe root contains a symlink")
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	if missing {
		return nil, os.ErrNotExist
	}
	return os.OpenRoot(path)
}

func (f maintenanceFixture) open(t *testing.T) *Maintenance {
	t.Helper()
	m, err := acquireMaintenance(context.Background(), f.hooks)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f maintenanceFixture) writeInstalled(t *testing.T, installed Installed) {
	t.Helper()
	body, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.paths.State, "installed.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceReceiptMissingThenCAS(t *testing.T) {
	f := newMaintenanceFixture(t)
	m := f.open(t)
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := prior.Value(); present || got != (Installed{}) {
		t.Fatalf("missing receipt returned value=%+v present=%t", got, present)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err != nil {
		t.Fatal(err)
	}
	current, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := current.Value(); !present || got != f.installed {
		t.Fatalf("published receipt mismatch: value=%+v present=%t", got, present)
	}
}

func TestMaintenanceReceiptCASExactPriorToNext(t *testing.T) {
	f := newMaintenanceFixture(t)
	f.writeInstalled(t, f.installed)
	m := f.open(t)
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := f.installed
	next.Version = "v1.0.1"
	if err := m.CompareAndSwapInstalled(context.Background(), prior, next); err != nil {
		t.Fatal(err)
	}
	current, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := current.Value(); !present || got != next {
		t.Fatalf("published receipt mismatch: value=%+v present=%t", got, present)
	}
}

func TestMaintenanceCASRejectsReceiptFromAnotherSession(t *testing.T) {
	f := newMaintenanceFixture(t)
	first := f.open(t)
	defer first.Close()
	prior, err := first.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foreign := &Maintenance{paths: f.paths, hooks: f.hooks}
	if err := foreign.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("foreign session receipt accepted")
	}
}

func TestMaintenanceCASRejectsReceiptAfterSessionClose(t *testing.T) {
	f := newMaintenanceFixture(t)
	m := f.open(t)
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("closed-session receipt accepted")
	}
}

func TestMaintenanceCASRejectsStaleSameSizeReceiptBytes(t *testing.T) {
	f := newMaintenanceFixture(t)
	f.writeInstalled(t, f.installed)
	m := f.open(t)
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.paths.State, "installed.json")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := f.installed.SHA256
	replacement := "a" + old[1:]
	if old[0] == 'a' {
		replacement = "b" + old[1:]
	}
	body = []byte(strings.Replace(string(body), old, replacement, 1))
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("same-size receipt mutation accepted")
	}
}

func TestMaintenanceCASRejectsReceiptPathReplacement(t *testing.T) {
	f := newMaintenanceFixture(t)
	f.writeInstalled(t, f.installed)
	m := f.open(t)
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.paths.State, "installed.json")); err != nil {
		t.Fatal(err)
	}
	f.writeInstalled(t, f.installed)
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("replacement receipt identity accepted")
	}
}

func TestMaintenanceCASRejectsPendingCollisionAndPreservesIt(t *testing.T) {
	f := newMaintenanceFixture(t)
	m := f.open(t)
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	foreign := []byte("foreign pending state")
	path := filepath.Join(f.paths.State, "installation-receipt.pending")
	if err := os.WriteFile(path, foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("pending collision accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(foreign) {
		t.Fatalf("foreign pending state was changed: body=%q err=%v", got, err)
	}
}

func TestMaintenanceConflictFilesRefuseAcquisitionAndPreserveState(t *testing.T) {
	for _, conflict := range maintenanceConflictFixtures() {
		t.Run(conflict.name, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			f.writeInstalled(t, f.installed)
			installedPath := filepath.Join(f.paths.State, "installed.json")
			before, err := os.ReadFile(installedPath)
			if err != nil {
				t.Fatal(err)
			}
			conflictPath := conflict.path(f.paths)
			if err := os.MkdirAll(filepath.Dir(conflictPath), 0700); err != nil {
				t.Fatal(err)
			}
			marker := []byte("pre-existing native transaction")
			if err := os.WriteFile(conflictPath, marker, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := acquireMaintenance(context.Background(), f.hooks); err == nil {
				t.Fatal("acquisition accepted unreconciled native state")
			}
			after, err := os.ReadFile(installedPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("installed receipt changed: err=%v", err)
			}
			got, err := os.ReadFile(conflictPath)
			if err != nil || string(got) != string(marker) {
				t.Fatalf("conflict file changed: body=%q err=%v", got, err)
			}
			release, err := f.hooks.acquireLock(f.paths.State)
			if err != nil {
				t.Fatalf("failed acquisition retained updater lock: %v", err)
			}
			release()
		})
	}
}

func TestMaintenanceConflictFilesAreRecheckedBeforeCAS(t *testing.T) {
	for _, conflict := range maintenanceConflictFixtures() {
		t.Run(conflict.name, func(t *testing.T) {
			f := newMaintenanceFixture(t)
			f.writeInstalled(t, f.installed)
			m := f.open(t)
			defer m.Close()
			prior, err := m.Receipt(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			installedPath := filepath.Join(f.paths.State, "installed.json")
			before, err := os.ReadFile(installedPath)
			if err != nil {
				t.Fatal(err)
			}
			conflictPath := conflict.path(f.paths)
			if err := os.MkdirAll(filepath.Dir(conflictPath), 0700); err != nil {
				t.Fatal(err)
			}
			marker := []byte("introduced after maintenance acquisition")
			if err := os.WriteFile(conflictPath, marker, 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
				t.Fatal("CAS accepted newly appeared native state")
			}
			after, err := os.ReadFile(installedPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("installed receipt changed: err=%v", err)
			}
			got, err := os.ReadFile(conflictPath)
			if err != nil || string(got) != string(marker) {
				t.Fatalf("conflict file changed: body=%q err=%v", got, err)
			}
		})
	}
}

type maintenanceConflictFixture struct {
	name string
	path func(nativePaths) string
}

func maintenanceConflictFixtures() []maintenanceConflictFixture {
	return []maintenanceConflictFixture{
		{name: "state_activation_json", path: func(p nativePaths) string { return filepath.Join(p.State, "activation.json") }},
		{name: "state_activation_pending", path: func(p nativePaths) string { return filepath.Join(p.State, "activation.pending") }},
		{name: "state_request_json", path: func(p nativePaths) string { return filepath.Join(p.State, "request.json") }},
		{name: "state_launch_json", path: func(p nativePaths) string { return filepath.Join(p.State, "launch.json") }},
		{name: "state_launch_pending", path: func(p nativePaths) string { return filepath.Join(p.State, "launch.pending") }},
		{name: "state_installed_pending", path: func(p nativePaths) string { return filepath.Join(p.State, "installed.pending") }},
		{name: "state_installation_receipt_pending", path: func(p nativePaths) string { return filepath.Join(p.State, "installation-receipt.pending") }},
		{name: "control_request_json", path: func(p nativePaths) string { return filepath.Join(controlDirectory(p.Data), "request.json") }},
	}
}

func TestMaintenanceControlDirectoryAbsenceAllowedAndUnsafeAncestryRefused(t *testing.T) {
	t.Run("absent canonical directory", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		m := f.open(t)
		defer m.Close()
		prior, err := m.Receipt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err != nil {
			t.Fatalf("absence of canonical control directory was not allowed: %v", err)
		}
	})
	t.Run("unsafe ancestry", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(f.paths.Data, "selfupdate")); err != nil {
			t.Fatal(err)
		}
		if _, err := acquireMaintenance(context.Background(), f.hooks); err == nil {
			t.Fatal("unsafe control ancestry was accepted")
		}
	})
	t.Run("unsafe ancestry appears before CAS", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		m := f.open(t)
		defer m.Close()
		prior, err := m.Receipt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(t.TempDir(), "outside")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(f.paths.Data, "selfupdate")); err != nil {
			t.Fatal(err)
		}
		if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
			t.Fatal("CAS accepted unsafe control ancestry")
		}
		if _, err := os.Lstat(filepath.Join(f.paths.State, "installed.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CAS published receipt despite unsafe ancestry: %v", err)
		}
	})
}

func TestMaintenancePendingCleanupPreservesUnexpectedReplacement(t *testing.T) {
	f := newMaintenanceFixture(t)
	foreign := []byte("replacement owned by another actor")
	f.hooks.beforePublish = func(_ context.Context, root *os.Root) error {
		if err := root.Remove("installation-receipt.pending"); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(f.paths.State, "installation-receipt.pending"), foreign, 0600)
	}
	m, err := acquireMaintenance(context.Background(), f.hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("injected publication interruption accepted")
	}
	got, err := os.ReadFile(filepath.Join(f.paths.State, "installation-receipt.pending"))
	if err != nil || string(got) != string(foreign) {
		t.Fatalf("unexpected replacement was not preserved: body=%q err=%v", got, err)
	}
}

func TestMaintenanceRejectsUnsafeReceiptAndExecutable(t *testing.T) {
	t.Run("receipt symlink", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		if err := os.Symlink("outside", filepath.Join(f.paths.State, "installed.json")); err != nil {
			t.Fatal(err)
		}
		m := f.open(t)
		defer m.Close()
		if _, err := m.Receipt(context.Background()); err == nil {
			t.Fatal("unsafe receipt accepted")
		}
	})
	t.Run("executable hash mismatch", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		m := f.open(t)
		defer m.Close()
		prior, err := m.Receipt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		next := f.installed
		next.SHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := m.CompareAndSwapInstalled(context.Background(), prior, next); err == nil {
			t.Fatal("mismatched executable accepted")
		}
	})
	t.Run("executable symlink", func(t *testing.T) {
		f := newMaintenanceFixture(t)
		m := f.open(t)
		defer m.Close()
		prior, err := m.Receipt(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(f.paths.Bin, f.paths.Binary)); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("outside", filepath.Join(f.paths.Bin, f.paths.Binary)); err != nil {
			t.Fatal(err)
		}
		if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
			t.Fatal("symlink manager executable accepted")
		}
	})
}

func TestMaintenanceCloseIsIdempotentAndUnlocks(t *testing.T) {
	f := newMaintenanceFixture(t)
	m := f.open(t)
	if _, err := m.Receipt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Receipt(context.Background()); err == nil {
		t.Fatal("closed session remained usable")
	}
	release, err := f.hooks.acquireLock(m.paths.State)
	if err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	release()
}

func TestMaintenanceLockIsExclusiveAcrossIndependentAcquisitions(t *testing.T) {
	f := newMaintenanceFixture(t)
	first := f.open(t)
	if _, err := acquireMaintenance(context.Background(), f.hooks); err == nil {
		t.Fatal("second acquisition bypassed updater operation lock")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := f.open(t)
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceLockCrossProcessHelper(t *testing.T) {
	if path := os.Getenv("MUI_MAINTENANCE_LOCK_HELPER"); path != "" {
		release, err := launcher.AcquireManager(path)
		if err == nil {
			release()
			t.Fatal("second process acquired an already-held updater lock")
		}
		return
	}
	f := newMaintenanceFixture(t)
	m := f.open(t)
	defer m.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestMaintenanceLockCrossProcessHelper$")
	command.Env = append(os.Environ(), "MUI_MAINTENANCE_LOCK_HELPER="+f.paths.State)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child lock exclusion check failed: %v: %s", err, output)
	}
}

func TestMaintenanceCancellationAfterLockReleasesIt(t *testing.T) {
	f := newMaintenanceFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	hooks := f.hooks
	acquire := hooks.acquireLock
	hooks.acquireLock = func(path string) (func(), error) {
		release, err := acquire(path)
		if err == nil {
			cancel()
		}
		return release, err
	}
	if _, err := acquireMaintenance(ctx, hooks); err == nil {
		t.Fatal("cancellation after lock acquisition was ignored")
	}
	m := f.open(t)
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceCASReportsShortWriteAndCleansPending(t *testing.T) {
	f := newMaintenanceFixture(t)
	f.hooks.writePending = func(_ context.Context, file *os.File, body []byte) error {
		if len(body) < 2 {
			return errors.New("test body unexpectedly short")
		}
		_, err := file.Write(body[:len(body)/2])
		return err
	}
	m, err := acquireMaintenance(context.Background(), f.hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(context.Background(), prior, f.installed); err == nil {
		t.Fatal("short write was accepted")
	}
	if _, err := os.Lstat(filepath.Join(f.paths.State, "installation-receipt.pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned partial pending receipt was not cleaned: %v", err)
	}
}

func TestMaintenanceCASFenceFailureReportsPublicationUncertain(t *testing.T) {
	f := newMaintenanceFixture(t)
	f.hooks.fence = func(context.Context, *os.Root) error { return errors.New("injected fence failure") }
	m, err := acquireMaintenance(context.Background(), f.hooks)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = m.CompareAndSwapInstalled(context.Background(), prior, f.installed)
	if err == nil || !strings.Contains(err.Error(), "publication uncertain") {
		t.Fatalf("fence failure was not reported as uncertain: %v", err)
	}
	current, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, present := current.Value(); !present || got != f.installed {
		t.Fatalf("published receipt not readable after uncertain fence: %+v %t", got, present)
	}
}

func TestMaintenanceReceiptAndCASHonorCancellation(t *testing.T) {
	f := newMaintenanceFixture(t)
	m := f.open(t)
	defer m.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Receipt(ctx); err == nil {
		t.Fatal("cancelled receipt read succeeded")
	}
	prior, err := m.Receipt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := m.CompareAndSwapInstalled(ctx, prior, f.installed); err == nil {
		t.Fatal("cancelled compare-and-swap succeeded")
	}
}

// launcherAcquireManager is kept as a tiny package-local test helper so the
// fixtures exercise the same kernel lock contract as production acquisition.
func launcherAcquireManager(path string) (func(), error) {
	return launcher.AcquireManager(path)
}
