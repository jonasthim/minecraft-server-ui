package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

type startupTestNative struct {
	autostartCalls, starts, stops int
	running                       bool
}

func (n *startupTestNative) Autostart(context.Context, map[string]domain.LaunchSpec) error {
	n.autostartCalls++
	return nil
}
func (n *startupTestNative) Start(context.Context, string, domain.LaunchSpec) (domain.ProcessIdentity, error) {
	n.starts++
	return domain.ProcessIdentity{}, errors.New("unexpected start")
}
func (n *startupTestNative) Stop(context.Context, domain.ProcessIdentity) error {
	n.stops++
	return nil
}
func (n *startupTestNative) Identity(_ context.Context, id string) (domain.ProcessIdentity, error) {
	if n.running {
		return domain.ProcessIdentity{InstanceID: id, PID: 42}, nil
	}
	return domain.ProcessIdentity{}, os.ErrNotExist
}
func (n *startupTestNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	if n.running {
		return supervisor.Observation{State: domain.StateRunning, ProcessRunning: true}, nil
	}
	return supervisor.Observation{State: domain.StateStopped}, nil
}
func (n *startupTestNative) WithStopped(_ context.Context, _ string, fn func() error) error {
	if n.running {
		return domain.E(domain.CodeInstanceRunning, "live JVM")
	}
	return fn()
}
func (*startupTestNative) SetAutostart(context.Context, string, bool) error { return nil }

func TestStartupRecoversExportsAndDispatchesRetainedLifecycleJob(t *testing.T) {
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
	exportDir := filepath.Join(dirs.Backups, ".exports")
	if err = os.MkdirAll(exportDir, 0700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(exportDir, "0123456789abcdef0123456789abcdef.zip")
	if err = os.WriteFile(orphan, []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	// Inject a crash with an uncommitted config journal and a lifecycle boot
	// guard. Recovery must restore config before lifecycle may remove the guard,
	// even though this instance retains a queued operation.
	detail, _ := instances.Get(t.Context(), "alpha")
	var owner string
	if err = database.QueryRow(`SELECT ownership_token FROM instance_settings WHERE instance_id='alpha'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	propertiesPath := filepath.Join(dirs.Server, "server.properties")
	before, err := os.ReadFile(propertiesPath)
	if err != nil {
		t.Fatal(err)
	}
	eula, err := os.ReadFile(filepath.Join(dirs.Server, "eula.txt"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]any{"owner_token": owner, "before_updated_at": detail.Instance.UpdatedAt, "after_updated_at": detail.Instance.UpdatedAt.Add(time.Second), "before": map[string]any{"properties": map[string]any{"exists": true, "data": before}, "eula": map[string]any{"exists": true, "data": eula}}}
	pendingJSON, _ := json.Marshal(snapshot)
	configJournal := filepath.Join(dirs.State, "config.pending.json")
	if err = os.WriteFile(configJournal, pendingJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(propertiesPath, []byte("uncommitted=true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO lifecycle_activations(instance_id,generation,installation_json) VALUES('alpha','uninstalled','{}')`); err != nil {
		t.Fatal(err)
	}
	if err = launcher.MarkActivationPending(t.Context(), dir, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dirs.State, "launch.json"), []byte("uncommitted"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable) VALUES('retained','alpha','lifecycle','queued','2026-10-16T00:00:00Z',?, ?,1)`, `{"operation":"start","actor_id":1}`, actor.ID); err != nil {
		t.Fatal(err)
	}
	operations := lifecycle.New(lifecycle.Config{DB: database, DataDir: dir, Instances: instances, Jobs: queue, Native: native})
	if err = operations.RegisterJobs(); err != nil {
		t.Fatal(err)
	}
	worlds := world.New(database, instances, paths, queue)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = reconcileStartup(t.Context(), queue, native, instances, operations, worlds, startupBackupStub{}, guard, log, false); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan survived startup: %v", err)
	}
	if _, err = os.Stat(configJournal); !os.IsNotExist(err) {
		t.Fatalf("instance journal not recovered first: %v", err)
	}
	restored, err := os.ReadFile(propertiesPath)
	if err != nil || string(restored) != string(before) {
		t.Fatalf("uncommitted config was imported: %s %v", restored, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), dir, "alpha"); err != nil {
		t.Fatalf("lifecycle marker not reconciled after config: %v", err)
	}
	for deadline := time.Now().Add(time.Second); ; {
		job, err := queue.Get(t.Context(), "retained")
		if err != nil {
			t.Fatal(err)
		}
		if job.State == domain.JobFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("retained job was not dispatched: %+v", job)
		}
		time.Sleep(time.Millisecond)
	}
	if native.starts != 0 || native.autostartCalls != 1 {
		t.Fatalf("unexpected native calls: %+v", native)
	}
	var message string
	if err = database.QueryRow(`SELECT message FROM job_logs WHERE job_id='retained'`).Scan(&message); err != nil || message != "explicit Minecraft EULA acceptance is required before start" {
		t.Fatalf("retained payload not executed faithfully: %s %v", message, err)
	}
}

func TestConsoleFailureShutsDownHTTPAndReturnsSafeError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	shutdown := 0
	err := watchConsole(t.Context(), func(context.Context) error { return errors.New("private database path") }, func(ctx context.Context) error {
		shutdown++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("shutdown unbounded")
		}
		return nil
	}, log)
	if shutdown != 1 || domain.AsError(err).Code != domain.CodeUnavailable || err.Error() == "private database path" {
		t.Fatalf("shutdown=%d error=%v", shutdown, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = watchConsole(ctx, func(ctx context.Context) error { return ctx.Err() }, func(context.Context) error { shutdown++; return nil }, log)
	if err != nil || shutdown != 1 {
		t.Fatalf("normal cancellation treated as failure: %v shutdown=%d", err, shutdown)
	}
}

func TestStartupRecoveryDefersLiveLinuxInstanceWithoutStoppingIt(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dir := t.TempDir()
	paths, _ := instance.NewPaths(dir)
	queue := jobs.New(database, nil)
	defer queue.Stop(context.Background())
	native := &startupTestNative{running: true}
	guard := &stoppedMutationGuard{queue: queue, native: native}
	instances := instance.New(database, paths, guard, instance.WithRecoveryMutationGuard(guard))
	if _, err = instances.Create(t.Context(), domain.User{ID: 1, Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	dirs, _ := paths.For("alpha")
	journal := filepath.Join(dirs.State, "config.pending.json")
	if err = os.WriteFile(journal, []byte("pending from prior startup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := instances.Reconcile(t.Context())
	if err != nil || len(result.Deferred) != 1 || result.Deferred[0].Code != domain.CodeInstanceRunning {
		t.Fatalf("live config was not deferred: %+v %v", result, err)
	}
	if native.starts != 0 || native.stops != 0 {
		t.Fatalf("recovery changed live lifecycle: %+v", native)
	}
	if _, err = os.Stat(journal); err != nil {
		t.Fatalf("deferred config journal changed: %v", err)
	}
	if err = queue.Resume(); err != nil {
		t.Fatal(err)
	}
}

type unavailableStopped struct{}

func (unavailableStopped) WithStopped(context.Context, string, func() error) error {
	return domain.Wrap(domain.CodeUnavailable, "native systemd lifecycle verification unavailable", errors.New("private host path"))
}
func TestNativeAdmissionFailureHasActionableSafeJobOutput(t *testing.T) {
	guard := &stoppedMutationGuard{native: unavailableStopped{}}
	err := guard.withNativeStopped(t.Context(), "alpha", func() error { t.Fatal("callback entered without native admission"); return nil })
	authored := domain.AsError(err)
	if authored.Code != domain.CodeUnavailable || authored.Cause != nil || !strings.Contains(authored.Message, "Install or repair") || strings.Contains(authored.Message, "private host path") {
		t.Fatalf("native error unsafe or unhelpful: %v", err)
	}
	guard.native = &startupTestNative{}
	providerFailure := domain.E(domain.CodeUnavailable, "verified distribution installation failed")
	if err = guard.withNativeStopped(t.Context(), "alpha", func() error { return providerFailure }); err != providerFailure {
		t.Fatalf("provider error mislabeled: %v", err)
	}
}
