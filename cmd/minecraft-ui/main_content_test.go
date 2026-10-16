package main

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type startupContentStub struct{ reconcile func(context.Context) error }

func (s startupContentStub) Reconcile(ctx context.Context) error {
	if s.reconcile != nil {
		return s.reconcile(ctx)
	}
	return nil
}

type contentStartupNative struct {
	startupTestNative
	steps *[]string
}

func (n *contentStartupNative) WithStopped(ctx context.Context, id string, fn func() error) error {
	if n.steps != nil {
		*n.steps = append(*n.steps, "instance")
	}
	return n.startupTestNative.WithStopped(ctx, id, fn)
}
func TestStartupContentOrderingFailureAndRetainedJob(t *testing.T) {
	for _, failure := range []string{"", "content", "missing-content"} {
		t.Run(failure, func(t *testing.T) {
			database, e := db.OpenMemory(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			defer database.Close()
			actor, e := db.NewUsers(database).Create(t.Context(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
			if e != nil {
				t.Fatal(e)
			}
			dir := t.TempDir()
			paths, e := instance.NewPaths(dir)
			if e != nil {
				t.Fatal(e)
			}
			queue := jobs.New(database, nil)
			defer queue.Stop(context.Background())
			native := &contentStartupNative{}
			guard := &stoppedMutationGuard{queue: queue, native: native}
			instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
			if _, e = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); e != nil {
				t.Fatal(e)
			}
			dirs, _ := paths.For("alpha")
			orphanDir := filepath.Join(dirs.Backups, ".exports")
			os.MkdirAll(orphanDir, 0700)
			orphan := filepath.Join(orphanDir, "0123456789abcdef0123456789abcdef.zip")
			os.WriteFile(orphan, []byte("interrupted"), 0600)
			dispatched := make(chan struct{}, 1)
			if e = queue.Register("proof", func(context.Context, *jobs.Execution) error { dispatched <- struct{}{}; return nil }); e != nil {
				t.Fatal(e)
			}
			if _, e = database.Exec("INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable) VALUES('retained','alpha','proof','queued','2026-10-16T00:00:00Z','{}',?,1)", actor.ID); e != nil {
				t.Fatal(e)
			}
			steps := []string{}
			native.steps = &steps
			backups := startupBackupStub{leases: func(context.Context) error { steps = append(steps, "leases"); return nil }, restores: func(context.Context) error { steps = append(steps, "restores"); return nil }}
			var contents startupContent = startupContentStub{reconcile: func(ctx context.Context) error {
				if !reflect.DeepEqual(steps, []string{"leases", "restores"}) {
					t.Fatalf("content recovery outside frozen startup order: %v", steps)
				}
				if native.autostartCalls != 1 {
					t.Fatal("native prior processes not reconciled")
				}
				if _, e := os.Stat(orphan); e != nil {
					t.Fatal("world recovery ran before content")
				}
				j, e := queue.Get(ctx, "retained")
				if e != nil || j.State != domain.JobQueued {
					t.Fatal("retained job dispatched before content recovery", j, e)
				}
				if e = queue.WithPausedInstance(ctx, "alpha", func() error { return nil }); e != nil {
					t.Fatal("content recovery not paused", e)
				}
				steps = append(steps, "content")
				if failure == "content" {
					return errors.New("injected content recovery failure")
				}
				return nil
			}}
			if failure == "missing-content" {
				contents = nil
			}
			operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: dir, Instances: instances, Jobs: queue, Native: native})
			worlds := world.New(database, instances, paths, queue)
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			e = reconcileStartup(t.Context(), queue, native, instances, operations, worlds, backups, contents, guard, log, true)
			if failure != "" {
				if e == nil {
					t.Fatal("failed content recovery admitted startup")
				}
				if native.autostartCalls != 1 || native.starts != 0 || native.stops != 0 {
					t.Fatal("failed recovery changed native lifecycle", native)
				}
				if e = queue.WithPausedInstance(t.Context(), "alpha", func() error { return nil }); e != nil {
					t.Fatal("queue resumed after failed content recovery", e)
				}
				for _, step := range steps {
					if step == "instance" {
						t.Fatal("instance reconciliation followed failed content recovery")
					}
				}
				if _, e = os.Stat(orphan); e != nil {
					t.Fatal("later recovery ran after failure")
				}
				select {
				case <-dispatched:
					t.Fatal("retained job dispatched after failure")
				default:
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				if len(steps) < 4 || steps[0] != "leases" || steps[1] != "restores" || steps[2] != "content" || steps[3] != "instance" {
					t.Fatal("startup order", steps)
				}
				if _, e = os.Stat(orphan); !os.IsNotExist(e) {
					t.Fatal("world recovery missing", e)
				}
				select {
				case <-dispatched:
				case <-time.After(time.Second):
					t.Fatal("retained job not dispatched after complete recovery")
				}
				if native.autostartCalls != 2 {
					t.Fatal("Windows autostart not reached")
				}
			}
		})
	}
}
func TestCombinedReadinessRunsInsideExistingNativeBoundary(t *testing.T) {
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	queue := jobs.New(database, nil)
	defer queue.Stop(context.Background())
	if e = queue.StartPaused(t.Context()); e != nil {
		t.Fatal(e)
	}
	native := &contentBoundaryNative{}
	steps := []string{}
	guard := &stoppedMutationGuard{queue: queue, native: native, ready: func(context.Context, string) error {
		if !native.inside {
			t.Fatal("readiness outside stopped boundary")
		}
		steps = append(steps, "backup", "content")
		return domain.E(domain.CodeConflict, "pending content")
	}}
	if e = guard.WithStopped(t.Context(), "alpha", func() error { t.Fatal("pending content admitted ordinary mutation"); return nil }); e == nil {
		t.Fatal("pending content admitted mutation")
	}
	if native.entries != 1 || !reflect.DeepEqual(steps, []string{"backup", "content"}) {
		t.Fatal("nested or skipped boundary", native.entries, steps)
	}
	if e = guard.WithStoppedForRecovery(t.Context(), "alpha", func() error {
		if !native.inside {
			t.Fatal("recovery outside native boundary")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if native.entries != 2 || len(steps) != 2 {
		t.Fatal("recovery entered ordinary readiness")
	}
}

type contentBoundaryNative struct {
	inside  bool
	entries int
}

func (n *contentBoundaryNative) WithStopped(_ context.Context, _ string, fn func() error) error {
	if n.inside {
		return errors.New("nested native admission")
	}
	n.inside = true
	n.entries++
	defer func() { n.inside = false }()
	return fn()
}
