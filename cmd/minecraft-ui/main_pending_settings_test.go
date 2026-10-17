package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

type noPendingNative struct{ t *testing.T }

func (n noPendingNative) WithStopped(context.Context, string, func() error) error {
	n.t.Error("DB-only pending settings entered the native stopped guard")
	return errors.New("native process may be running")
}

func TestPendingSettingsCompositionRetainsAdmissionAndRecoveryGates(t *testing.T) {
	for _, scenario := range []string{"ready", "startup-blocked", "ordinary-repair", "update-repair"} {
		t.Run(scenario, func(t *testing.T) {
			database, err := db.OpenMemory(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			queue := jobs.New(database, nil)
			defer queue.Stop(context.Background())
			if err := queue.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			var order []string
			blocked := errors.New("repair required")
			guard := &stoppedMutationGuard{queue: queue, native: noPendingNative{t}}
			if scenario == "startup-blocked" {
				guard.blocked = blocked
			}
			guard.ready = func(ctx context.Context, id string) error {
				order = append(order, "ordinary")
				if scenario == "ordinary-repair" {
					return blocked
				}
				return nil
			}
			guard.updateReady = func(ctx context.Context, id string) error {
				order = append(order, "update")
				if scenario == "update-repair" {
					return blocked
				}
				return nil
			}
			err = guard.WithPendingSettings(t.Context(), "alpha", func() error {
				order = append(order, "mutation")
				// The callback retains the same-instance reservation even though it
				// intentionally makes no observation or change of the native JVM.
				err := queue.WithIdle(t.Context(), "alpha", func() error {
					t.Error("concurrent same-instance mutation admitted")
					return nil
				})
				if domain.AsError(err).Code != domain.CodeInstanceBusy {
					t.Errorf("reservation absent: %v", err)
				}
				return nil
			})
			want := map[string][]string{"ready": {"ordinary", "update", "mutation"}, "startup-blocked": nil, "ordinary-repair": {"ordinary"}, "update-repair": {"ordinary", "update"}}[scenario]
			if !reflect.DeepEqual(order, want) || (scenario == "ready") != (err == nil) {
				t.Fatalf("guard order %v want %v; error %v", order, want, err)
			}
		})
	}
}
