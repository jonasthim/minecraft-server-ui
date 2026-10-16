package world

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func reconcileFixture(t *testing.T) (*Service, string) {
	t.Helper()
	s, base := exportFixture(t)
	if _, err := s.db.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('alpha','Alpha','paper','1.21.9',21,1024,25565)`); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(filepath.Dir(base), "backups", ".exports")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestReconcileExportsRemovesOnlyOwnedCrashOrphans(t *testing.T) {
	s, dir := reconcileFixture(t)
	orphan := strings.Repeat("a", 32) + ".zip"
	for _, name := range []string{orphan, "manual.zip", strings.Repeat("B", 32) + ".zip", strings.Repeat("c", 32) + ".ZIP", ".export-state"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("possibly incomplete crash ZIP"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	directory := strings.Repeat("d", 32) + ".zip"
	if err := os.Mkdir(filepath.Join(dir, directory), 0700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(dir))), "unknown", "backups", ".exports")
	if err := os.MkdirAll(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unknown, orphan), []byte("unowned instance"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileExports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, orphan)); !os.IsNotExist(err) {
		t.Fatal("crash orphan remains", err)
	}
	for _, name := range []string{"manual.zip", strings.Repeat("B", 32) + ".zip", strings.Repeat("c", 32) + ".ZIP", ".export-state", directory} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err != nil {
			t.Fatal("unrecognized entry removed", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(unknown, orphan)); err != nil {
		t.Fatal("unowned instance changed", err)
	}
	if err := s.ReconcileExports(context.Background()); err != nil {
		t.Fatal("repeat reconciliation", err)
	}
}

func TestReconcileExportsPreservesLinksAndTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := reconcileFixture(t)
			target := filepath.Join(t.TempDir(), "external.zip")
			if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(dir, strings.Repeat("e", 32)+".zip")
			link := os.Symlink
			if kind == "hardlink" {
				link = os.Link
			}
			if err := link(target, name); err != nil {
				t.Skip(err)
			}
			if err := s.ReconcileExports(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(name); err != nil {
				t.Fatal("link removed", err)
			}
			if raw, err := os.ReadFile(target); err != nil || string(raw) != "preserve" {
				t.Fatal("target changed", string(raw), err)
			}
		})
	}
}

func TestReconcileExportsRefusesActiveTransfersGlobally(t *testing.T) {
	s, _ := exportFixture(t)
	a, err := s.Capture(context.Background(), operator, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	other, dir := reconcileFixture(t)
	orphan := filepath.Join(dir, strings.Repeat("f", 32)+".zip")
	if err := os.WriteFile(orphan, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = other.ReconcileExports(context.Background()); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("active transfer did not prevent reconciliation", err)
	}
	if _, err = os.Stat(orphan); err != nil {
		t.Fatal("cleanup ran during active transfer", err)
	}
	if _, err = a.Read(make([]byte, 4)); err != nil {
		t.Fatal("active transfer damaged", err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	if err = other.ReconcileExports(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan not removed after transfer closed", err)
	}
}

func TestReconcileExportsHonorsCancellationBoundsAndRootLinks(t *testing.T) {
	for _, tc := range []string{"cancel", "bound", "root-link"} {
		t.Run(tc, func(t *testing.T) {
			s, dir := reconcileFixture(t)
			ctx := context.Background()
			switch tc {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "bound":
				s.limits.entries = 1
				for _, name := range []string{"one", "two"} {
					if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "root-link":
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Skip(err)
				}
			}
			if err := s.ReconcileExports(ctx); err == nil {
				t.Fatal("unsafe or canceled cleanup accepted")
			}
		})
	}
}

func TestReconcileExportsWithPausedQueuePreservesDurableJob(t *testing.T) {
	s, dir := reconcileFixture(t)
	if _, err := s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload) VALUES('waiting-world','alpha','world-test','queued','2026-10-16T00:00:00Z','{}')`); err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(s.db, nil)
	if err := queue.Register("world-test", func(context.Context, *jobs.Execution) error { t.Error("paused job dispatched"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := queue.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	s.reservation = queue
	orphan := filepath.Join(dir, strings.Repeat("a", 32)+".zip")
	if err := os.WriteFile(orphan, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	other, _ := exportFixture(t)
	active, err := other.Capture(context.Background(), operator, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if err := s.ReconcileExports(context.Background()); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("active archive did not prevent startup cleanup", err)
	}
	if _, err := os.Stat(orphan); err != nil {
		t.Fatal("active archive allowed orphan deletion", err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileExports(context.Background()); err != nil {
		t.Fatal("startup recovery blocked by queued work", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan remains", err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM jobs WHERE id='waiting-world'`).Scan(&state); err != nil || state != "queued" {
		t.Fatal("durable job changed", state, err)
	}
}

func TestReconciliationExcludesNewCaptureAcrossServices(t *testing.T) {
	s, _ := reconcileFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.db.SetMaxOpenConns(1)
	// Hold the real DB connection so reconciliation pauses in its first query
	// after taking global admission, without a production test hook.
	connection, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	done := make(chan error, 1)
	waitsBefore := s.db.Stats().WaitCount
	go func() { done <- s.ReconcileExports(ctx) }()
	for s.db.Stats().WaitCount == waitsBefore {
		// Observing database admission cannot contend with the export mutex:
		// probing TryLock here could make reconciliation itself refuse admission.
		select {
		case reconcileErr := <-done:
			t.Fatal("reconciliation ended before database admission", reconcileErr)
		default:
		}
		if ctx.Err() != nil {
			t.Fatal("reconciliation never acquired admission", ctx.Err())
		}
		runtime.Gosched()
	}
	other, _ := exportFixture(t)
	a, err := other.Capture(ctx, operator, "alpha")
	if a != nil {
		a.Close()
	}
	if closeErr := connection.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if reconcileErr := <-done; reconcileErr != nil {
		t.Fatal(reconcileErr)
	}
	if domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("capture admitted during global reconciliation", err)
	}
}
