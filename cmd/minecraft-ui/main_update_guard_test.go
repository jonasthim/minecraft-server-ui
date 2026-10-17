package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func TestUpdateJobGateCannotBypassOrdinaryRecovery(t *testing.T) {
	for _, scenario := range []string{"ordinary-pending", "confirmed-job", "job-denied", "strict-fallback"} {
		t.Run(scenario, func(t *testing.T) {
			database, err := db.OpenMemory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			actor, err := db.NewUsers(database).Create(t.Context(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
			if err != nil {
				t.Fatal(err)
			}
			paths, err := instance.NewPaths(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			queue := jobs.New(database, nil)
			defer queue.Stop(context.Background())
			native := &contentBoundaryNative{}
			guard := &stoppedMutationGuard{queue: queue, native: native}
			instances := instance.New(database, paths, guard)
			_, err = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
			if err != nil {
				t.Fatal(err)
			}
			ordinaryPending := errors.New("ordinary backup/content/pack repair remains pending")
			updatePending := errors.New("update repair remains pending")
			guard.ready = func(context.Context, string) error {
				if !native.inside {
					return errors.New("readiness escaped native boundary")
				}
				if scenario == "ordinary-pending" {
					return ordinaryPending
				}
				return nil
			}
			guard.updateReady = func(context.Context, string) error { return updatePending }
			jobChecks, mutations := 0, 0
			if scenario != "strict-fallback" {
				guard.updateJobReady = func(ctx context.Context, e *jobs.Execution, id string) error {
					jobChecks++
					identity, err := e.CheckCurrent(ctx)
					if err != nil {
						return err
					}
					if !native.inside || identity.InstanceID != id || identity.Kind != "instance_rollback" || identity.RequesterID != actor.ID {
						return errors.New("job gate did not receive original admitted execution")
					}
					if scenario == "job-denied" {
						return updatePending
					}
					return nil
				}
			}
			// An ordinary edit must never borrow the job-only exception.
			if err := guard.WithStopped(t.Context(), "alpha", func() error { mutations++; return nil }); err == nil {
				t.Fatal("ordinary edit bypassed pending recovery")
			}
			if mutations != 0 || jobChecks != 0 {
				t.Fatal("ordinary edit entered job exception")
			}
			finished := make(chan error, 1)
			if err := queue.Register("instance_rollback", func(ctx context.Context, e *jobs.Execution) error {
				err := guard.WithStoppedForJob(ctx, e, func(string) error { mutations++; return nil })
				finished <- err
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := queue.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "instance_rollback", RequestedBy: actor.ID, Payload: []byte("{}")}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if scenario == "confirmed-job" {
					if err != nil || mutations != 1 || jobChecks != 1 {
						t.Fatalf("original job exception failed: %v, mutations=%d, checks=%d", err, mutations, jobChecks)
					}
				} else if err == nil || mutations != 0 {
					t.Fatalf("recovery gate bypassed: %v, mutations=%d", err, mutations)
				}
				if scenario == "ordinary-pending" && (!errors.Is(err, ordinaryPending) || jobChecks != 0) {
					t.Fatal("job exception bypassed ordinary repair", err, jobChecks)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("original job deadlocked in readiness admission")
			}
		})
	}
}
