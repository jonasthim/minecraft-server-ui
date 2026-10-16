package main

import (
	"context"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/api"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/web"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

type startupBackupStub struct{ leases, restores func(context.Context) error }

func (s startupBackupStub) ReconcileLeases(ctx context.Context) error {
	if s.leases != nil {
		return s.leases(ctx)
	}
	return nil
}
func (s startupBackupStub) ReconcileRestores(ctx context.Context) error {
	if s.restores != nil {
		return s.restores(ctx)
	}
	return nil
}
func TestStartupBackupOrderingAndFailuresRetainQueuedWork(t *testing.T) {
	for _, failure := range []string{"", "leases", "restores"} {
		t.Run(failure, func(t *testing.T) {
			database, err := db.OpenMemory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			actor, err := db.NewUsers(database).Create(t.Context(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			paths, _ := instance.NewPaths(dir)
			queue := jobs.New(database, nil)
			defer queue.Stop(context.Background())
			native := &startupTestNative{}
			guard := &stoppedMutationGuard{queue: queue, native: native}
			instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
			if _, err = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
				t.Fatal(err)
			}
			dispatched := make(chan struct{}, 1)
			queue.Register("proof", func(context.Context, *jobs.Execution) error { dispatched <- struct{}{}; return nil })
			database.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable)VALUES('retained','alpha','proof','queued','2026-10-16T00:00:00Z','{}',?,1)`, actor.ID)
			steps := []string{}
			step := func(name string) func(context.Context) error {
				return func(ctx context.Context) error {
					if native.autostartCalls != 1 {
						t.Fatal("backup recovery preceded native prior-process wait")
					}
					j, err := queue.Get(ctx, "retained")
					if err != nil || j.State != domain.JobQueued {
						t.Fatal("work dispatched before saving/restore recovery")
					}
					steps = append(steps, name)
					if failure == name {
						return errors.New("injected recovery failure")
					}
					return nil
				}
			}
			backups := startupBackupStub{leases: step("leases"), restores: step("restores")}
			operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: dir, Instances: instances, Jobs: queue, Native: native})
			worlds := world.New(database, instances, paths, queue)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			err = reconcileStartup(t.Context(), queue, native, instances, operations, worlds, backups, startupContentStub{}, startupContentStub{}, startupContentStub{}, guard, log, true)
			if failure != "" {
				if err == nil {
					t.Fatal("failed backup recovery admitted startup")
				}
				if native.autostartCalls != 1 {
					t.Fatal("autostart followed failed recovery")
				}
				if err = queue.WithPausedInstance(t.Context(), "alpha", func() error { return nil }); err != nil {
					t.Fatal("queue was not left paused", err)
				}
				select {
				case <-dispatched:
					t.Fatal("failed recovery dispatched retained work")
				default:
				}
				if failure == "leases" && len(steps) != 1 {
					t.Fatal("restore recovery followed failed leases")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(steps) != 2 || steps[0] != "leases" || steps[1] != "restores" {
					t.Fatal(steps)
				}
				select {
				case <-dispatched:
				case <-time.After(time.Second):
					t.Fatal("retained work did not dispatch after recovery")
				}
				if native.autostartCalls != 2 {
					t.Fatal("validated Windows autostart missing")
				}
			}
		})
	}
}
func TestStoppedGuardReadinessAppliesOnlyToOrdinaryMutation(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	queue := jobs.New(database, nil)
	defer queue.Stop(context.Background())
	native := &startupTestNative{}
	calls := 0
	guard := &stoppedMutationGuard{queue: queue, native: native, ready: func(context.Context, string) error {
		calls++
		return domain.E(domain.CodeUnavailable, "restore pending")
	}}
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = guard.WithStopped(t.Context(), "alpha", func() error { t.Fatal("ordinary mutation bypassed recovery gate"); return nil }); err == nil {
		t.Fatal("ordinary mutation accepted")
	}
	if calls != 1 {
		t.Fatal("readiness not checked")
	}
	if err = guard.WithStoppedForRecovery(t.Context(), "alpha", func() error { return nil }); err != nil {
		t.Fatal("startup recovery blocked by ordinary readiness", err)
	}
	if calls != 1 {
		t.Fatal("recovery entered ordinary readiness gate")
	}
}

// Opt-in browser harness: domain state must be created through shipping APIs/UI.
// Only native process/service availability is substituted; no game is started.
func TestBackupBrowserHarness(t *testing.T) {
	address := os.Getenv("MINECRAFT_UI_BACKUP_BROWSER_ADDR")
	if address == "" {
		t.Skip("opt-in browser harness")
	}
	dir := t.TempDir()
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, _ := instance.NewPaths(dir)
	queue := jobs.New(database, nil)
	defer queue.Stop(context.Background())
	native := &backupBrowserNative{}
	guard := &stoppedMutationGuard{queue: queue, native: native}
	instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	controls := console.New(database, paths, native, instances, queue)
	worlds := world.New(database, instances, paths, queue)
	backups := backup.New(backup.Config{DB: database, Paths: paths, Instances: instances, Jobs: queue, Console: controls, Native: native, Worlds: worlds})
	operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: dir, Instances: instances, Jobs: queue, Native: native, Providers: []domain.DistributionProvider{vanilla.New(dir, nil)}})
	if err = operations.RegisterJobs(); err != nil {
		t.Fatal(err)
	}
	guard.ready = backups.CheckReady
	if err = backups.Register(); err != nil {
		t.Fatal(err)
	}
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = backups.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = queue.Resume(); err != nil {
		t.Fatal(err)
	}
	identity := auth.NewService(database, auth.Config{BaseURL: "http://" + address, InsecureCookies: true}, nil)
	frontend, err := web.Dist()
	if err != nil {
		t.Fatal(err)
	}
	handler := api.NewRouter(api.Dependencies{DB: database, Web: frontend, Mount: func(r chi.Router) {
		mountAPIWithFeatures(r, identity, queue, instances, func(r chi.Router) {
			api.MountBackups(r, backups)
			api.MountLifecycle(r, operations)
			api.MountWorlds(r, worlds)
		})
	}})
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: time.Second}
	defer server.Close()
	t.Logf("backup browser harness http://%s (native fixture; no game)", address)
	if err = server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}

type backupBrowserNative struct{ startupTestNative }

func (n *backupBrowserNative) ArmSaveLease(context.Context, domain.ProcessIdentity, string, time.Time) error {
	return errors.New("fixture does not run games")
}
func (n *backupBrowserNative) ReleaseSaveLease(context.Context, domain.ProcessIdentity, string) error {
	return errors.New("fixture does not run games")
}
func (n *backupBrowserNative) Command(context.Context, domain.ProcessIdentity, string) error {
	return errors.New("fixture does not run games")
}
