package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type testNative struct {
	starts     int
	running    bool
	autoErr    error
	stopErr    error
	stops      int
	command    func(string) error
	inspectErr error
}

func (n *testNative) WithStopped(_ context.Context, _ string, fn func() error) error {
	if n.running {
		return errors.New("running")
	}
	return fn()
}
func (n *testNative) Start(_ context.Context, id string, _ domain.LaunchSpec) (domain.ProcessIdentity, error) {
	n.starts++
	n.running = true
	return domain.ProcessIdentity{InstanceID: id, PID: 12}, nil
}
func (n *testNative) Stop(context.Context, domain.ProcessIdentity) error {
	n.stops++
	if n.stopErr != nil {
		return n.stopErr
	}
	n.running = false
	return nil
}
func (n *testNative) Identity(_ context.Context, id string) (domain.ProcessIdentity, error) {
	if !n.running {
		return domain.ProcessIdentity{}, os.ErrNotExist
	}
	return domain.ProcessIdentity{InstanceID: id, PID: 12}, nil
}
func (n *testNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{ProcessRunning: n.running, State: domain.StateRunning}, n.inspectErr
}
func (n *testNative) SetAutostart(context.Context, string, bool) error { return n.autoErr }

func (n *testNative) Command(_ context.Context, _ domain.ProcessIdentity, command string) error {
	if n.command != nil {
		return n.command(command)
	}
	return errors.New("console unavailable")
}
func (n *testNative) ReleaseSaveLease(context.Context, domain.ProcessIdentity, string) error {
	return nil
}

type testGuard struct{ n *testNative }

func (g testGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	return g.n.WithStopped(ctx, id, fn)
}
func (g testGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { return g.n.WithStopped(ctx, id, func() error { return fn(id) }) })
}

type testRuntime struct{}

func (testRuntime) EnsureRequirements(context.Context, domain.LaunchRequirements, io.Writer) (domain.JavaRuntime, error) {
	return domain.JavaRuntime{ID: "runtime", Major: 17}, nil
}
func (testRuntime) Get(context.Context, string) (domain.JavaRuntime, error) {
	return domain.JavaRuntime{ID: "runtime", Major: 17}, nil
}

type testProvider struct{ fail bool }

func (testProvider) Flavor() domain.Flavor { return domain.FlavorVanilla }
func (testProvider) Resolve(_ context.Context, s domain.VersionSelection) (domain.InstallPlan, error) {
	return domain.InstallPlan{Selection: s, Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17}}, nil
}
func (p testProvider) Install(_ context.Context, _ string, _ domain.InstallPlan, _ domain.JavaRuntime, _ io.Writer) (domain.LaunchSpec, error) {
	if p.fail {
		return domain.LaunchSpec{}, errors.New("download failed")
	}
	return domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "immutable.jar", "nogui"}}, nil
}
func fixture(t *testing.T) (*Service, domain.User, *testNative) {
	t.Helper()
	ctx := context.Background()
	database, err := db.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	actor, err := db.NewUsers(database).Create(ctx, db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	n := &testNative{}
	q := jobs.New(database, nil)
	dir := t.TempDir()
	paths, err := instance.NewPaths(dir)
	if err != nil {
		t.Fatal(err)
	}
	ins := instance.New(database, paths, testGuard{n}, instance.WithJobMutationGuard(testGuard{n}))
	_, err = ins.Create(ctx, *actor, instance.Draft{Instance: domain.Instance{ID: "test", Name: "Test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{DB: database, DataDir: dir, Instances: ins, Jobs: q, Native: n, Runtime: testRuntime{}, Providers: []domain.DistributionProvider{testProvider{}}})
	s.seal = func(context.Context, string, domain.LaunchSpec) error { return nil }
	if err = s.RegisterJobs(); err != nil {
		t.Fatal(err)
	}
	if err = q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop(ctx) })
	return s, *actor, n
}
func waitJob(t *testing.T, s *Service, j jobs.Job) jobs.Job {
	t.Helper()
	for limit := time.Now().Add(3 * time.Second); time.Now().Before(limit); {
		out, err := s.queue.Get(context.Background(), j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if out.State != domain.JobRunning && out.State != domain.JobQueued {
			return out
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
	return jobs.Job{}
}
func TestInstallPersistsActivationAndStartRequiresExplicitEULA(t *testing.T) {
	s, u, n := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	preview, err := s.Preview(ctx, "test", selection(d))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Enqueue(ctx, u, "test", Request{Operation: "install", PreviewID: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitJob(t, s, j)
	if finished.State != domain.JobSucceeded {
		t.Fatalf("install: %+v", finished)
	}
	in, err := s.loadInstallation(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if in.Spec.Args[0] != "-Xms512M" || in.Spec.Args[1] != "-Xmx1024M" {
		t.Fatal(in.Spec.Args)
	}
	d, err = s.instances.Get(ctx, "test")
	if err != nil || d.Provisioning != "installed" || d.Instance.JavaRuntimeID != "runtime" || d.EULA.Accepted {
		t.Fatalf("detail=%+v err=%v", d, err)
	}
	j, err = s.Enqueue(ctx, u, "test", Request{Operation: "start"})
	if err != nil {
		t.Fatal(err)
	}
	finished = waitJob(t, s, j)
	if finished.State != domain.JobFailed || n.starts != 0 {
		t.Fatalf("start bypassed EULA: %+v starts=%d", finished, n.starts)
	}
}
func TestViewerAndUnconfirmedUninstallRejected(t *testing.T) {
	s, u, _ := fixture(t)
	u.Role = domain.RoleViewer
	if _, err := s.Enqueue(context.Background(), u, "test", Request{Operation: "start"}); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	u.Role = domain.RoleAdmin
	if _, err := s.Enqueue(context.Background(), u, "test", Request{Operation: "uninstall"}); err == nil {
		t.Fatal("unconfirmed uninstall admitted")
	}
}

type paperTestProvider struct{ testProvider }

func (paperTestProvider) Flavor() domain.Flavor { return domain.FlavorPaper }
func TestInitialLoaderPinAndHistoryFence(t *testing.T) {
	s, u, _ := fixture(t)
	ctx := context.Background()
	s.providers[domain.FlavorPaper] = paperTestProvider{}
	if _, err := s.db.Exec(`UPDATE instances SET flavor='paper',loader_version='' WHERE id='test'`); err != nil {
		t.Fatal(err)
	}
	chosen := domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.20.1", LoaderVersion: "123"}
	preview, err := s.Preview(ctx, "test", chosen)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Enqueue(ctx, u, "test", Request{Operation: "install", PreviewID: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitJob(t, s, j); result.State != domain.JobSucceeded {
		t.Fatalf("initial pin failed %+v", result)
	}
	d, err := s.instances.Get(ctx, "test")
	if err != nil || d.Instance.LoaderVersion != "123" {
		t.Fatalf("pin=%s err=%v", d.Instance.LoaderVersion, err)
	}
	chosen.LoaderVersion = "124"
	if _, err = s.Preview(ctx, "test", chosen); err == nil {
		t.Fatal("repin allowed")
	}
	if _, err = s.db.Exec(`UPDATE instances SET loader_version='' WHERE id='test'; UPDATE instance_settings SET provisioning='not_installed' WHERE instance_id='test'; DELETE FROM lifecycle_installations WHERE instance_id='test'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Preview(ctx, "test", chosen); err == nil {
		t.Fatal("history bypass allowed")
	}
}
func TestStaleInstallPreviewCannotActivate(t *testing.T) {
	s, u, _ := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	preview, err := s.Preview(ctx, "test", selection(d))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE instances SET updated_at='2099-01-01T00:00:00Z' WHERE id='test'`); err != nil {
		t.Fatal(err)
	}
	j, err := s.Enqueue(ctx, u, "test", Request{Operation: "install", PreviewID: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out := waitJob(t, s, j); out.State != domain.JobFailed {
		t.Fatal(out)
	}
	if _, err = s.loadInstallation(ctx, "test"); domain.AsError(err).Code != domain.CodeInstanceNotInstalled {
		t.Fatal(err)
	}
}
func TestAutostartBindsEditedMemoryAndSkipsQueuedWork(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	plan, _ := testProvider{}.Resolve(ctx, selection(d))
	old := installation{Generation: "old", Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-Xms512M", "-Xmx1024M", "-jar", "immutable.jar"}}}
	if err := s.saveInstallation(ctx, "test", old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE instances SET memory_mib=4096 WHERE id='test'; UPDATE instance_settings SET eula_accepted=1 WHERE instance_id='test'; INSERT INTO lifecycle_autostart(instance_id,desired,applied) VALUES('test',1,1)`); err != nil {
		t.Fatal(err)
	}
	plans, err := s.AutostartPlans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if plans["test"].Args[1] != "-Xmx4096M" {
		t.Fatal(plans)
	}
	current, err := s.loadInstallation(ctx, "test")
	if err != nil || current.Spec.Args[1] != "-Xmx4096M" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if _, err = s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at) VALUES('pending','test','lifecycle','queued','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	plans, err = s.AutostartPlans(ctx)
	if err != nil || len(plans) != 0 {
		t.Fatalf("plans=%v err=%v", plans, err)
	}
}
func TestStartupVerificationOnlyQueuesFreshRunningGames(t *testing.T) {
	s, _, n := fixture(t)
	ctx := context.Background()
	if err := s.VerifyRunningAfterReconcile(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='lifecycle_verify'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	n.running = true
	if err := s.VerifyRunningAfterReconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE kind='lifecycle_verify' AND requested_by=0`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if n.starts != 0 {
		t.Fatal("startup verification started a game")
	}
}

func TestStopVerificationFailureAndEmergencyNativeFailureAreHonest(t *testing.T) {
	s, u, n := fixture(t)
	ctx := context.Background()
	n.running = true
	zero := 0
	job, err := s.Enqueue(ctx, u, "test", Request{Operation: "stop", CountdownSeconds: &zero})
	if err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job)
	if result.State != domain.JobFailed || !n.running || n.stops != 0 {
		t.Fatalf("normal stop bypassed missing console: %+v native=%+v", result, n)
	}
	classifiedSupervisor, err := supervisor.New(supervisor.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	n.stopErr = classifiedSupervisor.Stop(canceled, domain.ProcessIdentity{InstanceID: "test"})
	if supervisor.StopFailureCategory(n.stopErr) != supervisor.StopFailureLockWait {
		t.Fatalf("test error was not classified: %v", n.stopErr)
	}
	job, err = s.Enqueue(ctx, u, "test", Request{Operation: "stop", Emergency: true, Confirmation: "test"})
	if err != nil {
		t.Fatal(err)
	}
	result = waitJob(t, s, job)
	if result.State != domain.JobFailed || !n.running || n.stops != 1 {
		t.Fatalf("native failure misreported: %+v native=%+v", result, n)
	}
	logs, err := s.queue.Logs(ctx, job.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, line := range logs {
		if line.Message == "Native stop diagnostic: lock_wait_failed." {
			count++
		}
		if strings.Contains(line.Message, "context canceled") {
			t.Fatalf("raw cause leaked to job log: %q", line.Message)
		}
	}
	if count != 1 {
		t.Fatalf("got %d fixed stop diagnostic log entries: %+v", count, logs)
	}
}
func TestStartedProcessWithoutReadinessRemainsRunningAndReportsIt(t *testing.T) {
	s, u, n := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	plan, _ := testProvider{}.Resolve(ctx, selection(d))
	if err := s.saveInstallation(ctx, "test", installation{Generation: "old", Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "immutable.jar"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE instance_settings SET eula_accepted=1 WHERE instance_id='test'`); err != nil {
		t.Fatal(err)
	}
	job, err := s.Enqueue(ctx, u, "test", Request{Operation: "start"})
	if err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job)
	if result.State != domain.JobFailed || !n.running || n.stops != 0 {
		t.Fatalf("readiness failure: %+v native=%+v", result, n)
	}
	var output string
	if err := s.db.QueryRow(`SELECT message FROM job_logs WHERE job_id=? ORDER BY id DESC LIMIT 1`, job.ID).Scan(&output); err != nil || !strings.Contains(output, "remains running") {
		t.Fatalf("missing honest process diagnostic: %s err=%v", output, err)
	}
}
func TestStartupVerificationFreshAcknowledgementAndGenerationFence(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "changed_generation"}[stale], func(t *testing.T) {
			s, _, n := fixture(t)
			ctx := context.Background()
			n.running = true
			paths, err := instance.NewPaths(s.dataDir)
			if err != nil {
				t.Fatal(err)
			}
			dirs, _ := paths.For("test")
			logPath := filepath.Join(dirs.Logs, "console.log")
			if err = os.WriteFile(logPath, []byte("old Done message\n"), 0600); err != nil {
				t.Fatal(err)
			}
			commands := 0
			n.command = func(command string) error {
				commands++
				f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = f.WriteString("[12:00:00] [Server thread/INFO]: There are 0 of a max of 20 players online: \n")
				return err
			}
			s.control = console.New(s.db, paths, n, s.instances, s.queue)
			identity, _ := n.Identity(ctx, "test")
			if stale {
				identity.PID = 99
			}
			payload, _ := json.Marshal(verifyInput{Identity: identity})
			job, err := s.queue.Enqueue(ctx, jobs.Request{InstanceID: "test", Kind: "lifecycle_verify", Payload: payload, Cancellable: true})
			if err != nil {
				t.Fatal(err)
			}
			result := waitJob(t, s, job)
			if stale {
				if result.State != domain.JobFailed || commands != 0 {
					t.Fatalf("stale verification: %+v commands=%d", result, commands)
				}
			} else if result.State != domain.JobSucceeded || commands == 0 {
				t.Fatalf("fresh verification: %+v commands=%d", result, commands)
			}
			if n.starts != 0 || n.stops != 0 {
				t.Fatal("verification changed process lifecycle")
			}
		})
	}
}
func TestFailedProviderReinstallPreservesCommittedInstallationAndWorld(t *testing.T) {
	s, u, _ := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	plan, _ := testProvider{}.Resolve(ctx, selection(d))
	old := installation{Generation: "old", Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "old.jar"}}}
	if err := s.saveInstallation(ctx, "test", old); err != nil {
		t.Fatal(err)
	}
	paths, _ := instance.NewPaths(s.dataDir)
	dirs, _ := paths.For("test")
	world := filepath.Join(dirs.Server, "world")
	if err := os.MkdirAll(world, 0700); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(world, "level.dat")
	if err := os.WriteFile(saved, []byte("keep-world"), 0600); err != nil {
		t.Fatal(err)
	}
	s.providers[domain.FlavorVanilla] = testProvider{fail: true}
	preview, err := s.Preview(ctx, "test", selection(d))
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Enqueue(ctx, u, "test", Request{Operation: "install", PreviewID: preview.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitJob(t, s, job); result.State != domain.JobFailed {
		t.Fatal(result)
	}
	current, err := s.loadInstallation(ctx, "test")
	if err != nil || current.Generation != "old" {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	data, err := os.ReadFile(saved)
	if err != nil || string(data) != "keep-world" {
		t.Fatalf("world=%s err=%v", data, err)
	}
}

func TestCancellingVerifiedCountdownNeverCallsNativeStop(t *testing.T) {
	s, u, n := fixture(t)
	ctx := context.Background()
	n.running = true
	paths, _ := instance.NewPaths(s.dataDir)
	dirs, _ := paths.For("test")
	logPath := filepath.Join(dirs.Logs, "console.log")
	if err := os.WriteFile(logPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	warned := make(chan struct{})
	n.command = func(command string) error {
		response := "There are 0 of a max of 20 players online: "
		if strings.HasPrefix(command, "say ") {
			response = "[Server] " + strings.TrimPrefix(command, "say ")
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("[12:00:00] [Server thread/INFO]: " + response + "\n")
		if strings.HasPrefix(command, "say ") {
			close(warned)
		}
		return err
	}
	s.control = console.New(s.db, paths, n, s.instances, s.queue)
	countdown := 60
	job, err := s.Enqueue(ctx, u, "test", Request{Operation: "stop", CountdownSeconds: &countdown})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-warned:
	case <-time.After(time.Second):
		t.Fatal("warning not sent")
	}
	if err = s.queue.Cancel(ctx, job.ID, u); err != nil {
		t.Fatal(err)
	}
	result := waitJob(t, s, job)
	if result.State != domain.JobCancelled || n.stops != 0 || !n.running {
		t.Fatalf("cancelled countdown stopped process: %+v native=%+v", result, n)
	}
}

func TestUninstallRetainsWorldsConfigAndImmutableGenerations(t *testing.T) {
	s, u, _ := fixture(t)
	ctx := context.Background()
	d, _ := s.instances.Get(ctx, "test")
	plan, _ := testProvider{}.Resolve(ctx, selection(d))
	if err := s.saveInstallation(ctx, "test", installation{Generation: "old", Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime"}}); err != nil {
		t.Fatal(err)
	}
	paths, _ := instance.NewPaths(s.dataDir)
	dirs, _ := paths.For("test")
	files := []string{filepath.Join(dirs.State, "launch.json"), filepath.Join(dirs.Server, "world", "level.dat"), filepath.Join(dirs.Server, "mods", "local.dat"), filepath.Join(dirs.Backups, "protected.zip")}
	for _, path := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	job, err := s.Enqueue(ctx, u, "test", Request{Operation: "uninstall", Confirmation: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if result := waitJob(t, s, job); result.State != domain.JobSucceeded {
		t.Fatal(result)
	}
	if _, err = os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatalf("active launch survived: %v", err)
	}
	for _, path := range files[1:] {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "keep" {
			t.Fatalf("retained data lost: %s %s %v", path, data, err)
		}
	}
	var generations int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM lifecycle_generations WHERE instance_id='test'`).Scan(&generations); err != nil || generations != 1 {
		t.Fatalf("generations=%d %v", generations, err)
	}
	state, err := s.State(ctx, "test")
	if err != nil || state.Installed || state.AutostartDesired || state.AutostartApplied == nil || *state.AutostartApplied {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestStatePreservesKnownRunningProcessWhenControlIsUnavailable(t *testing.T) {
	s, _, n := fixture(t)
	n.running = true
	n.inspectErr = errors.New("control channel unavailable")
	state, err := s.State(t.Context(), "test")
	if err != nil || !state.ProcessRunning || state.Process != "unknown" {
		t.Fatalf("known running JVM hidden by control error: %+v %v", state, err)
	}
}

type testPendingGuard struct{ queue *jobs.Service }

func (g testPendingGuard) WithPendingSettings(ctx context.Context, id string, fn func() error) error {
	return g.queue.WithIdle(ctx, id, fn)
}
func pendingLifecycleFixture(t *testing.T) (*Service, domain.User, *testNative, instance.Detail, string) {
	t.Helper()
	s, u, n, _, path := configActivationFixture(t)
	paths, _ := instance.NewPaths(s.dataDir)
	s.instances = instance.New(s.db, paths, testGuard{n}, instance.WithJobMutationGuard(testGuard{n}), instance.WithActivationCoordinator(s), instance.WithPendingSettingsGuard(testPendingGuard{s.queue}))
	if _, err := s.db.Exec(`UPDATE instance_settings SET eula_accepted=1 WHERE instance_id='test'`); err != nil {
		t.Fatal(err)
	}
	d, err := s.instances.Get(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.instances.SavePendingSettings(t.Context(), u, "test", instance.SavePendingSettings{PendingSettingsDraft: instance.PendingSettingsDraft{Name: "Pending", MemoryMiB: 4096, Config: d.Config}, ExpectedInstanceUpdatedAt: d.Instance.UpdatedAt.Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	return s, u, n, d, path
}
func TestOrdinaryStartConsumesPendingWithMemoryActivation(t *testing.T) {
	s, u, n, d, path := pendingLifecycleFixture(t)
	j, err := s.Enqueue(t.Context(), u, "test", Request{Operation: "start"})
	if err != nil {
		t.Fatal(err)
	}
	out := waitJob(t, s, j)
	// This fixture has no readiness transport; actual native launch is the boundary
	// asserted here. Its final readiness failure must not undo the committed edit.
	if out.State != domain.JobFailed || n.starts != 1 {
		t.Fatalf("did not launch with fixture readiness gap: %+v starts=%d", out, n.starts)
	}
	active, err := s.instances.Get(t.Context(), "test")
	if err != nil || active.Instance.Name != "Pending" || active.Instance.MemoryMiB != 4096 || !active.Instance.UpdatedAt.After(d.Instance.UpdatedAt) {
		t.Fatalf("active=%+v %v", active, err)
	}
	pending, err := s.instances.GetPendingSettings(t.Context(), "test")
	if err != nil || pending.Draft != nil || pending.Revision != 2 {
		t.Fatal(pending, err)
	}
	seal, err := os.ReadFile(path)
	if err != nil || string(seal) != "-Xmx4096M" {
		t.Fatalf("seal=%s %v", seal, err)
	}
	installed, err := s.loadInstallation(t.Context(), "test")
	if err != nil || installed.Spec.Args[1] != "-Xmx4096M" {
		t.Fatal(installed, err)
	}
}
func TestOrdinaryStartRefusesStaleOrRevokedPendingAuthor(t *testing.T) {
	for _, mode := range []string{"stale", "revoked", "sql", "audit", "seal", "commit", "file"} {
		t.Run(mode, func(t *testing.T) {
			s, u, n, d, path := pendingLifecycleFixture(t)
			var err error
			switch mode {
			case "stale":
				_, err = s.db.Exec(`UPDATE instances SET updated_at='2099-01-01T00:00:00Z' WHERE id='test'`)
			case "revoked":
				_, err = s.db.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'starter','admin','now','now'); UPDATE users SET role='viewer' WHERE id=1`)
				u = domain.User{ID: 2, Username: "starter", Role: domain.RoleAdmin}
			case "sql":
				_, err = s.db.Exec(`CREATE TRIGGER reject_settings BEFORE UPDATE ON instances BEGIN SELECT RAISE(ABORT,'injected settings failure'); END`)
			case "audit":
				_, err = s.db.Exec(`CREATE TRIGGER reject_pending_audit BEFORE INSERT ON audit_log WHEN new.action='instance.pending_settings.apply' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`)
			case "commit":
				_, err = s.db.Exec(`CREATE TABLE pending_commit_fixture(id INTEGER PRIMARY KEY, parent_id INTEGER REFERENCES pending_commit_fixture(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER reject_config_commit AFTER UPDATE ON instances BEGIN INSERT INTO pending_commit_fixture(id,parent_id) VALUES(1,999); END`)
			case "file":
				paths, _ := instance.NewPaths(s.dataDir)
				dirs, _ := paths.For("test")
				err = os.Remove(filepath.Join(dirs.Server, "eula.txt"))
				if err == nil {
					err = os.Mkdir(filepath.Join(dirs.Server, "eula.txt"), 0700)
				}
			case "seal":
				old := s.seal
				s.seal = func(ctx context.Context, id string, spec domain.LaunchSpec) error {
					if spec.Args[1] == "-Xmx4096M" {
						return errors.New("injected seal failure")
					}
					return old(ctx, id, spec)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			j, err := s.Enqueue(t.Context(), u, "test", Request{Operation: "start"})
			if err != nil {
				t.Fatal(err)
			}
			if out := waitJob(t, s, j); out.State != domain.JobFailed || n.starts != 0 {
				t.Fatalf("unsafe boot: %+v starts=%d", out, n.starts)
			}
			active, err := s.instances.Get(t.Context(), "test")
			if err != nil || active.Instance.Name != d.Instance.Name || active.Instance.MemoryMiB != 1024 {
				t.Fatal(active, err)
			}
			pending, err := s.instances.GetPendingSettings(t.Context(), "test")
			if err != nil || pending.Draft == nil || pending.Revision != 1 {
				t.Fatal(pending, err)
			}
			seal, err := os.ReadFile(path)
			if err != nil || string(seal) != "-Xmx1024M" {
				t.Fatalf("old seal lost: %s %v", seal, err)
			}
		})
	}
}

func TestAutostartPreparationPreservesPendingSettings(t *testing.T) {
	s, u, n, _, path := pendingLifecycleFixture(t)
	j, err := s.Enqueue(t.Context(), u, "test", Request{Operation: "autostart", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if out := waitJob(t, s, j); out.State != domain.JobSucceeded {
		t.Fatal(out)
	}
	pending, err := s.instances.GetPendingSettings(t.Context(), "test")
	if err != nil || pending.Draft == nil || pending.Revision != 1 || pending.Stale {
		t.Fatal(pending, err)
	}
	seal, err := os.ReadFile(path)
	if err != nil || string(seal) != "-Xmx1024M" || n.starts != 0 {
		t.Fatalf("autostart policy consumed draft: %s starts=%d %v", seal, n.starts, err)
	}
}
func TestUpdateStartPreservesPendingSettings(t *testing.T) {
	s, u, n, _, path := pendingLifecycleFixture(t)
	ctx := t.Context()
	if err := s.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(s.db, nil)
	s.queue = q
	if err := q.Register("instance_update", func(ctx context.Context, e *jobs.Execution) error {
		current, err := e.CheckCurrent(ctx)
		if err != nil {
			return err
		}
		installed, err := s.loadInstallation(ctx, "test")
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]any{"version": 1, "boot_attempted": true, "after": map[string]any{"generation": installed.Generation}})
		if _, err = s.db.ExecContext(ctx, `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES('pending-update','test','preview',?,?,'update','boot_attempted','backup',?,'now')`, current.JobID, u.ID, string(raw)); err != nil {
			return err
		}
		err = s.StartForUpdate(ctx, e, u, func(_ context.Context, _ string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
			return start()
		})
		if err == nil {
			return errors.New("fixture readiness gap was hidden")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	j, err := q.Enqueue(ctx, jobs.Request{InstanceID: "test", Kind: "instance_update", RequestedBy: u.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out := waitJob(t, s, j); out.State != domain.JobSucceeded {
		t.Fatal(out)
	}
	pending, err := s.instances.GetPendingSettings(ctx, "test")
	if err != nil || pending.Draft == nil || pending.Revision != 1 {
		t.Fatal(pending, err)
	}
	seal, err := os.ReadFile(path)
	if err != nil || string(seal) != "-Xmx1024M" || n.starts != 1 {
		t.Fatalf("update start consumed draft: %s starts=%d %v", seal, n.starts, err)
	}
}
