package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

func TestStartupSchedulesReconcileBeforeAutostartAndDispatch(t *testing.T) {
	for _, failure := range []string{"", "schedule", "missing"} {
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
			paths, err := instance.NewPaths(dir)
			if err != nil {
				t.Fatal(err)
			}
			queue := jobs.New(database, nil)
			defer queue.Stop(context.Background())
			native := &startupTestNative{}
			guard := &stoppedMutationGuard{queue: queue, native: native}
			instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
			if _, err = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
				t.Fatal(err)
			}
			dirs, _ := paths.For("alpha")
			exports := filepath.Join(dirs.Backups, ".exports")
			if err = os.MkdirAll(exports, 0700); err != nil {
				t.Fatal(err)
			}
			orphan := filepath.Join(exports, "0123456789abcdef0123456789abcdef.zip")
			if err = os.WriteFile(orphan, []byte("interrupted"), 0600); err != nil {
				t.Fatal(err)
			}
			dispatched := make(chan struct{}, 1)
			if err = queue.Register("proof", func(context.Context, *jobs.Execution) error { dispatched <- struct{}{}; return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err = database.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable) VALUES('schedule-retained','alpha','proof','queued','2026-10-16T00:00:00Z','{}',?,1)`, actor.ID); err != nil {
				t.Fatal(err)
			}
			reconciled := false
			var schedules startupContent = startupContentStub{reconcile: func(ctx context.Context) error {
				if native.autostartCalls != 1 {
					t.Fatal("schedule recovery followed Windows autostart")
				}
				if _, err := os.Stat(orphan); !os.IsNotExist(err) {
					t.Fatal("schedule recovery preceded export reconciliation", err)
				}
				if err := queue.WithPausedInstance(ctx, "alpha", func() error { return nil }); err != nil {
					t.Fatal("schedule recovery ran after dispatch resumed", err)
				}
				select {
				case <-dispatched:
					t.Fatal("retained work ran before schedule reconciliation")
				default:
				}
				reconciled = true
				if failure == "schedule" {
					return errors.New("injected schedule reconciliation failure")
				}
				return nil
			}}
			if failure == "missing" {
				schedules = nil
			}
			operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: dir, Instances: instances, Jobs: queue, Native: native})
			err = reconcileStartup(t.Context(), queue, native, instances, operations, world.New(database, instances, paths, queue), startupBackupStub{}, startupContentStub{}, startupContentStub{}, startupContentStub{}, schedules, guard, slog.New(slog.NewTextHandler(io.Discard, nil)), true)
			if failure != "" {
				if err == nil {
					t.Fatal("failed or missing schedule recovery admitted startup")
				}
				if native.autostartCalls != 1 {
					t.Fatal("Windows autostart followed failed schedule recovery")
				}
				if err = queue.WithPausedInstance(t.Context(), "alpha", func() error { return nil }); err != nil {
					t.Fatal("queue resumed on failure", err)
				}
				select {
				case <-dispatched:
					t.Fatal("queued work dispatched after failure")
				default:
				}
				return
			}
			if err != nil || !reconciled || native.autostartCalls != 2 {
				t.Fatal(reconciled, native.autostartCalls, err)
			}
			select {
			case <-dispatched:
			case <-time.After(time.Second):
				t.Fatal("retained job did not run after successful schedule reconciliation")
			}
		})
	}
}
