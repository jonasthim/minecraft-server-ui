package console

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type fakeNative struct {
	mu       sync.Mutex
	identity domain.ProcessIdentity
	running  bool
	write    func(string) error
}

func (f *fakeNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.identity, nil
}
func (f *fakeNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := domain.StateStopped
	if f.running {
		state = domain.StateRunning
	}
	return supervisor.Observation{State: state, ProcessRunning: f.running}, nil
}
func (f *fakeNative) Command(_ context.Context, _ domain.ProcessIdentity, text string) error {
	return f.write(text)
}
func (f *fakeNative) ReleaseSaveLease(context.Context, domain.ProcessIdentity, string) error {
	return f.write("save-on")
}

type fixtureInstances struct{ detail instance.Detail }

func (f fixtureInstances) Get(context.Context, string) (instance.Detail, error) { return f.detail, nil }
func (f fixtureInstances) Management(context.Context, string) (instance.ManagementConfig, error) {
	return instance.ManagementConfig{}, domain.E(domain.CodeUnavailable, "not configured")
}
func serviceFixture(t *testing.T) (*Service, *fakeNative, string) {
	t.Helper()
	h, path := logFixture(t)
	_ = os.WriteFile(path, []byte("old\n"), 0600)
	_, err := h.db.ExecContext(t.Context(), `INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'operator','operator','2026-10-16','2026-10-16')`)
	if err != nil {
		t.Fatal(err)
	}
	native := &fakeNative{identity: domain.ProcessIdentity{InstanceID: "test", PID: 123, StartIdentity: "one"}, running: true}
	native.write = func(text string) error {
		response := ""
		switch text {
		case "list":
			response = "There are 0 of a max of 20 players online: "
		case "say hello":
			response = "[Server] hello"
		case "save-all flush":
			response = "Saved the game"
		case "save-on":
			response = "Automatic saving is now enabled"
		}
		f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = f.WriteString("[12:00:00] [Server thread/INFO]: " + response + "\n")
		return e
	}
	queue := jobs.New(h.db, nil)
	s := New(h.db, h.paths, native, fixtureInstances{instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5"}}}, queue)
	if err = s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = queue.Stop(context.Background()) })
	return s, native, path
}
func waitConsoleJob(t *testing.T, q *jobs.Service, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		j, e := q.Get(t.Context(), id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State != domain.JobQueued && j.State != domain.JobRunning {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobs.Job{}
}
func TestReadinessFreshGenerationAndDeliberateStop(t *testing.T) {
	s, native, _ := serviceFixture(t)
	if err := s.Refresh(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	state, err := s.Status(t.Context(), "test")
	if err != nil || !state.ReadyForUse || !state.ProcessRunning {
		t.Fatalf("fresh native list: %+v %v", state, err)
	}
	native.mu.Lock()
	native.identity.StartIdentity = "two"
	native.mu.Unlock()
	state, err = s.Status(t.Context(), "test")
	if err != nil || state.ReadyForUse || !state.ProcessRunning {
		t.Fatalf("stale generation ready: %+v %v", state, err)
	}
	native.mu.Lock()
	native.running = false
	native.mu.Unlock()
	state, err = s.Status(t.Context(), "test")
	if err != nil || state.ReadyForUse || state.ProcessRunning || state.Status != "stopped" {
		t.Fatalf("stop readiness: %+v %v", state, err)
	}
}
func TestCommandQueueRoleAndExpiredSession(t *testing.T) {
	s, _, _ := serviceFixture(t)
	actor := domain.User{ID: 1, Username: "operator", Role: domain.RoleOperator}
	if _, err := s.EnqueueCommand(t.Context(), "test", "stop", actor); err == nil {
		t.Fatal("unsafe command queued")
	}
	viewer := actor
	viewer.Role = domain.RoleViewer
	if _, err := s.EnqueueCommand(t.Context(), "test", "list", viewer); err == nil {
		t.Fatal("viewer command queued")
	}
	job, err := s.EnqueueCommand(t.Context(), "test", "say hello", actor)
	if err != nil {
		t.Fatal(err)
	}
	j := waitConsoleJob(t, s.queue, job.ID)
	if j.State != domain.JobSucceeded {
		t.Fatalf("acknowledged command: %+v", j)
	}
	var audit string
	if err = s.db.QueryRowContext(t.Context(), `SELECT target FROM audit_log WHERE action='console.command.request'`).Scan(&audit); err != nil || audit != "say" {
		t.Fatalf("safe audit: %q %v", audit, err)
	}
	var captured *Session
	if err = s.queue.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(s.db, nil)
	s.queue = queue
	if err = queue.Register("typed", func(ctx context.Context, e *jobs.Execution) error {
		return s.WithJobSession(ctx, e, func(session *Session) error {
			captured = session
			copy := *session
			if _, err := copy.Flush(ctx); err == nil {
				return domain.E(domain.CodeInternal, "copied session accepted")
			}
			result, err := session.Flush(ctx)
			if err != nil || !result.Verified {
				return domain.E(domain.CodeInternal, "flush not verified")
			}
			_, err = session.ReleaseSaving(ctx, "lease")
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	job, err = queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "typed"})
	if err != nil {
		t.Fatal(err)
	}
	j = waitConsoleJob(t, queue, job.ID)
	if j.State != domain.JobSucceeded {
		t.Fatalf("typed command session: %+v", j)
	}
	if _, err = captured.Flush(t.Context()); err == nil {
		t.Fatal("expired session accepted")
	}
	raw, _ := json.Marshal(j)
	if string(raw) == "" {
		t.Fatal("job unavailable")
	}
}

func TestTypedPlayersUsesAcknowledgedCount(t *testing.T) {
	s, _, _ := serviceFixture(t)
	if err := s.queue.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(s.db, nil)
	s.queue = queue
	if err := queue.Register("players", func(ctx context.Context, e *jobs.Execution) error {
		return s.WithJobSession(ctx, e, func(session *Session) error {
			count, err := session.Players(ctx)
			if err != nil {
				return err
			}
			if count != 0 {
				return domain.E(domain.CodeInternal, "unexpected player count")
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	job, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "players"})
	if err != nil {
		t.Fatal(err)
	}
	if j := waitConsoleJob(t, queue, job.ID); j.State != domain.JobSucceeded {
		t.Fatalf("players job: %+v", j)
	}
}

func TestIdentityObservationLifetimeAndGeneration(t *testing.T) {
	s, native, _ := serviceFixture(t)
	var captured *Session
	err := s.withSession(t.Context(), "test", func(session *Session) error {
		captured = session
		identity, err := session.Identity(t.Context())
		if err != nil || identity != native.identity {
			t.Fatalf("identity %+v %v", identity, err)
		}
		identity.StartIdentity = "changed copy"
		identity, err = session.Identity(t.Context())
		if err != nil || identity.StartIdentity != "one" {
			t.Fatal("identity value aliased session")
		}
		copied := *session
		if _, err := copied.Identity(t.Context()); err == nil {
			t.Fatal("copied session identity accepted")
		}
		observation, err := session.ObservePlayers(t.Context())
		if err != nil || !observation.Known || observation.Count != 0 || observation.Source != "fresh_native_console_list" || observation.ObservedAt.IsZero() {
			t.Fatalf("observation %+v %v", observation, err)
		}
		native.mu.Lock()
		native.identity.StartIdentity = "replacement"
		native.mu.Unlock()
		if _, err := session.Identity(t.Context()); err == nil {
			t.Fatal("replaced generation accepted")
		}
		observation, err = session.ObservePlayers(t.Context())
		if err == nil || observation.Known {
			t.Fatal("unknown player count became known")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := captured.Identity(t.Context()); err == nil {
		t.Fatal("expired session identity accepted")
	}
	if observation, err := captured.ObservePlayers(t.Context()); err == nil || observation.Known {
		t.Fatal("expired observation accepted")
	}
}

func TestReleaseSavingAfterCancellationStillAttemptsWithMissingCursor(t *testing.T) {
	s, native, path := serviceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	native.write = func(text string) error {
		if text != "save-on" {
			t.Fatalf("unexpected cleanup command %q", text)
		}
		calls++
		return nil
	}
	err := s.withSession(ctx, "test", func(session *Session) error {
		cancel()
		if err := os.Remove(path); err != nil {
			return err
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		result, err := session.ReleaseSaving(cleanup, "lease")
		if err == nil || !result.Received || result.Verified || calls != 1 {
			t.Fatalf("cleanup %+v %v calls %d", result, err, calls)
		}
		if _, err := session.Identity(cleanup); err == nil {
			t.Fatal("cancelled ordinary method accepted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFailureIsVisibleAndRecoveryClearsIt(t *testing.T) {
	s, _, path := serviceFixture(t)
	if err := s.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	state, err := s.Status(t.Context(), "test")
	if err != nil || !state.LogsAvailable {
		t.Fatalf("capture status %+v %v", state, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(t.Context(), "test"); err == nil {
		t.Fatal("unsafe capture succeeded")
	}
	state, err = s.Status(t.Context(), "test")
	if err != nil || state.LogsAvailable || state.LogsReason == "" {
		t.Fatalf("hidden capture failure %+v %v", state, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("recovered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Capture(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	state, err = s.Status(t.Context(), "test")
	if err != nil || !state.LogsAvailable {
		t.Fatalf("capture recovery %+v %v", state, err)
	}
}

type managedFixtureInstances struct {
	fixtureInstances
	cfg              instance.ManagementConfig
	beforeManagement func()
}

func (f managedFixtureInstances) Management(context.Context, string) (instance.ManagementConfig, error) {
	if f.beforeManagement != nil {
		f.beforeManagement()
	}
	return f.cfg, nil
}

func TestModernSessionRejectsGenerationChangesBeforeEffects(t *testing.T) {
	for _, stage := range []string{"unchanged_generation", "before_verify", "management", "rpc.discover", "minecraft:server/status", "minecraft:gamerules", "minecraft:gamerules/update"} {
		t.Run(stage, func(t *testing.T) {
			s, native, _ := serviceFixture(t)
			replace := func() { native.mu.Lock(); native.identity.StartIdentity = "replacement"; native.mu.Unlock() }
			var callsMu sync.Mutex
			calls := map[string]int{}
			ruleValue := true
			cfg := rpcFixtureBeforeResponse(t, "3.1.0", func(method string, _ json.RawMessage) any {
				switch method {
				case "minecraft:server/status":
					return map[string]bool{"started": true}
				case "minecraft:gamerules":
					return []any{map[string]any{"key": "minecraft:pvp", "type": "boolean", "value": ruleValue}}
				case "minecraft:gamerules/update":
					ruleValue = false
					return map[string]any{"key": "minecraft:pvp", "type": "boolean", "value": false}
				}
				return nil
			}, func(method string) {
				callsMu.Lock()
				calls[method]++
				callsMu.Unlock()
				if method == stage {
					replace()
				}
			})
			detail := instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "26.3"}, Settings: instance.SettingsProjection{DesiredRuntime: []instance.DesiredRuntimeSetting{{Kind: "boolean_gamerule", Rule: "minecraft:pvp", Value: false, Supported: true, Scope: "native_server_rules"}}}}
			metadata := managedFixtureInstances{fixtureInstances: fixtureInstances{detail}, cfg: cfg}
			if stage == "management" {
				metadata.beforeManagement = replace
			}
			s.instances = metadata
			var state State
			err := s.withSession(t.Context(), "test", func(session *Session) error {
				if stage == "before_verify" {
					replace()
				}
				var err error
				state, err = session.VerifyReady(t.Context())
				return err
			})
			if stage == "unchanged_generation" {
				if err != nil || !state.ReadyForUse {
					t.Fatalf("original generation did not verify: %+v %v", state, err)
				}
			} else if err == nil || state.ReadyForUse {
				t.Fatalf("replacement was accepted: %+v %v", state, err)
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			allowedMutations := 0
			if stage == "minecraft:gamerules/update" || stage == "unchanged_generation" {
				allowedMutations = 1
			}
			if calls["minecraft:gamerules/update"] != allowedMutations {
				t.Fatalf("rule mutations after replacement: %v", calls)
			}
			if (stage == "before_verify" || stage == "management") && calls["rpc.discover"] != 0 {
				t.Fatalf("stale session connected to replacement: %v", calls)
			}
			if stage == "rpc.discover" && calls["minecraft:server/status"] != 0 {
				t.Fatalf("stale discovery used: %v", calls)
			}
			if stage == "minecraft:server/status" && calls["minecraft:gamerules"] != 0 {
				t.Fatalf("stale status used: %v", calls)
			}
			if stage == "minecraft:gamerules/update" && calls["minecraft:gamerules"] != 1 {
				t.Fatalf("stale mutation acknowledgement used for readback: %v", calls)
			}
		})
	}
}

func TestMSMP1219SessionUsesOfficialArrayDiscovery(t *testing.T) {
	s, _, _ := serviceFixture(t)
	cfg := rpcFixture(t, "1.0.0", func(method string, _ json.RawMessage) any {
		switch method {
		case "minecraft:server/status":
			return map[string]bool{"started": true}
		case "minecraft:gamerules":
			return []any{map[string]any{"key": "pvp", "type": "boolean", "value": "false"}}
		}
		return nil
	})
	detail := instance.Detail{Instance: domain.Instance{ID: "test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}, Settings: instance.SettingsProjection{DesiredRuntime: []instance.DesiredRuntimeSetting{{Kind: "boolean_gamerule", Rule: "pvp", Value: false, Supported: true, Scope: "native_server_rules"}}}}
	s.instances = managedFixtureInstances{fixtureInstances: fixtureInstances{detail}, cfg: cfg}
	var state State
	err := s.withSession(t.Context(), "test", func(session *Session) error {
		var err error
		state, err = session.VerifyReady(t.Context())
		return err
	})
	if err != nil || !state.ReadyForUse || !state.RequiredSettingsVerified || state.Source != "authenticated_msmp" {
		t.Fatalf("1.21.9 session discovery/readback: %+v %v", state, err)
	}
}
