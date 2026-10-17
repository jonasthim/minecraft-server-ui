package console

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

// Using mutation admission for a background read rejects an otherwise valid
// user command. A read must hold execution, while preserving durable admission.
func TestReadOnlyProbeAcceptsQueuedCommandDuringDelayedRPC(t *testing.T) {
	for _, probe := range []string{"refresh", "snapshot", "list", "positions"} {
		t.Run(probe, func(t *testing.T) {
			s, native, _ := serviceFixture(t)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			before := func(method string) {
				if method == "rpc.discover" {
					close(entered)
					<-release
				}
			}
			var cfg instance.ManagementConfig
			if probe == "refresh" {
				cfg = rpcFixtureBeforeResponse(t, "1.0.0", func(string, json.RawMessage) any {
					return map[string]any{"started": true}
				}, before)
			} else {
				cfg = playerRPCFixture(t, "1.0.0", false, func(string, json.RawMessage) any { return []any{} }, before)
			}
			s.instances = managedFixtureInstances{fixtureInstances: fixtureInstances{detail: instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}}}, cfg: cfg}
			commands := make(chan string, 1)
			write := native.write
			native.write = func(command string) error { commands <- command; return write(command) }
			done := make(chan error, 1)
			go func() {
				var err error
				switch probe {
				case "refresh":
					err = s.Refresh(t.Context(), "test")
				case "snapshot":
					_, err = s.PlayerSnapshot(t.Context(), "test")
				case "list":
					_, err = s.NativePlayerList(t.Context(), "test", "whitelist")
				case "positions":
					_, err = s.PlayerPositions(t.Context(), "test")
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("probe did not reach RPC")
			}
			job, err := s.EnqueueCommand(t.Context(), "test", "say hello", domain.User{ID: 1, Username: "operator", Role: domain.RoleOperator})
			if err != nil {
				t.Fatalf("read-only probe rejected user job admission: %v", err)
			}
			persisted, err := s.queue.Get(t.Context(), job.ID)
			if err != nil || persisted.State != domain.JobQueued {
				t.Fatalf("job not durably deferred: %+v %v", persisted, err)
			}
			select {
			case command := <-commands:
				t.Fatalf("command overlapped observation: %s", command)
			default:
			}
			once.Do(func() { close(release) })
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("probe did not finish")
			}
			if got := waitConsoleJob(t, s.queue, job.ID); got.State != domain.JobSucceeded {
				t.Fatalf("queued command failed after observation: %+v", got)
			}
			if got := <-commands; got != "say hello" {
				t.Fatal(got)
			}
		})
	}
}
