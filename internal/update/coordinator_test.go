package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

type coordinatorNative struct {
	inStopped    atomic.Bool
	policies     []bool
	failPolicyAt int
	policyHook   func(int, bool) error
	mu           sync.Mutex
	running      bool
	starts       int
}

func (n *coordinatorNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.running {
		return domain.ProcessIdentity{}, os.ErrNotExist
	}
	return domain.ProcessIdentity{InstanceID: "alpha", PID: 55, StartIdentity: "fixture"}, nil
}
func (n *coordinatorNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return supervisor.Observation{State: domain.StateRunning, ProcessRunning: n.running}, nil
}
func (n *coordinatorNative) WithStopped(ctx context.Context, id string, fn func() error) error {
	n.mu.Lock()
	live := n.running
	n.mu.Unlock()
	if live {
		return errors.New("fixture native is running")
	}
	n.inStopped.Store(true)
	defer n.inStopped.Store(false)
	return fn()
}
func (n *coordinatorNative) Start(context.Context, string, domain.LaunchSpec) (domain.ProcessIdentity, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.running = true
	n.starts++
	return domain.ProcessIdentity{InstanceID: "alpha", PID: 55}, nil
}
func (n *coordinatorNative) Stop(context.Context, domain.ProcessIdentity) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.running = false
	return nil
}
func (n *coordinatorNative) SetAutostart(ctx context.Context, id string, enabled bool) error {
	if n.inStopped.Load() {
		return errors.New("native policy nested under stopped lock")
	}
	n.mu.Lock()
	n.policies = append(n.policies, enabled)
	index := len(n.policies)
	fail := index == n.failPolicyAt
	hook := n.policyHook
	n.mu.Unlock()
	if hook != nil {
		if err := hook(index, enabled); err != nil {
			return err
		}
	}
	if fail {
		return errors.New("injected uncertain native policy failure")
	}
	return nil
}
func (n *coordinatorNative) ArmSaveLease(context.Context, domain.ProcessIdentity, string, time.Time) error {
	return errors.New("fixture never enables live backup")
}

type coordinatorGuard struct {
	updates *Service
	queue   *jobs.Service
	native  *coordinatorNative
}

func (g *coordinatorGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	return g.native.WithStopped(ctx, id, fn)
}
func (g *coordinatorGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error {
		return g.native.WithStopped(ctx, id, func() error {
			if g.updates != nil {
				if err := g.updates.checkOrdinaryReady(ctx, id); err != nil {
					return err
				}
				if err := g.updates.CheckJobReady(ctx, e, id); err != nil {
					return err
				}
			}
			return fn(id)
		})
	})
}
func (g *coordinatorGuard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.queue.WithPausedInstance(ctx, id, func() error { return g.native.WithStopped(ctx, id, fn) })
}

type coordinatorRuntime struct{ runtime domain.JavaRuntime }

func (r coordinatorRuntime) Get(context.Context, string) (domain.JavaRuntime, error) {
	return r.runtime, nil
}
func (r coordinatorRuntime) EnsureRequirements(context.Context, domain.LaunchRequirements, io.Writer) (domain.JavaRuntime, error) {
	return r.runtime, nil
}

type coordinatorProvider struct {
	flavor  domain.Flavor
	mu      sync.Mutex
	changed bool
}

func (p *coordinatorProvider) Flavor() domain.Flavor {
	if p.flavor != "" {
		return p.flavor
	}
	return domain.FlavorVanilla
}
func (p *coordinatorProvider) Resolve(ctx context.Context, selection domain.VersionSelection) (domain.InstallPlan, error) {
	p.mu.Lock()
	changed := p.changed
	p.mu.Unlock()
	sum := strings.Repeat("b", 64)
	if changed {
		sum = strings.Repeat("c", 64)
	}
	return domain.InstallPlan{Selection: selection, Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17}, Artifacts: []domain.Artifact{{ID: "server", Filename: "server.jar", Size: 6, Checksum: domain.Checksum{Algorithm: "sha256", Value: sum}}}}, nil
}
func (*coordinatorProvider) Install(ctx context.Context, id string, plan domain.InstallPlan, runtime domain.JavaRuntime, w io.Writer) (domain.LaunchSpec, error) {
	return domain.LaunchSpec{JavaRuntimeID: runtime.ID, JavaChecksum: runtime.Artifact.Checksum, Args: []string{"-jar", plan.Selection.MinecraftVersion + ".jar", "nogui"}}, nil
}

type coordinatorCatalog struct{ version *catalog.Version }

func (c coordinatorCatalog) Project(_ context.Context, _ catalog.Provider, id string) (catalog.Project, error) {
	if c.version != nil {
		return catalog.Project{ID: id, Kind: catalog.Mod}, nil
	}
	return catalog.Project{}, errors.New("no fixture catalog project")
}
func (c coordinatorCatalog) Version(context.Context, catalog.Provider, string, string) (catalog.Version, error) {
	if c.version != nil {
		return *c.version, nil
	}
	return catalog.Version{}, errors.New("no fixture catalog version")
}
func (coordinatorCatalog) Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error) {
	return catalog.VerifiedArtifact{}, errors.New("no fixture catalog download")
}

type coordinatorFixture struct {
	sealHook       func() error
	contentVersion *catalog.Version
	s              *Service
	queue          *jobs.Service
	guard          *coordinatorGuard
	native         *coordinatorNative
	provider       *coordinatorProvider
	actor          domain.User
	dirs           instance.Directories
	data           string
	sealCount      int
	failSeal       int
}

func newCoordinatorFixture(t *testing.T) *coordinatorFixture {
	return newCoordinatorFixtureFlavor(t, domain.FlavorVanilla)
}
func newCoordinatorFixtureFlavor(t *testing.T, flavor domain.Flavor) *coordinatorFixture {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	actor, err := db.NewUsers(database).Create(t.Context(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	paths, _ := instance.NewPaths(data)
	native := &coordinatorNative{}
	queue := jobs.New(database, nil)
	guard := &coordinatorGuard{queue: queue, native: native}
	instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard), instance.WithReconcileHold(func(ctx context.Context, id string) (bool, error) {
		if guard.updates == nil {
			return false, nil
		}
		return guard.updates.StartupBlocked(ctx, id)
	}))
	_, err = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: flavor, LoaderVersion: func() string {
		if flavor == domain.FlavorFabric {
			return "0.16.0"
		}
		return ""
	}(), MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := paths.For("alpha")
	os.MkdirAll(filepath.Join(dirs.Server, "world/region"), 0700)
	os.WriteFile(filepath.Join(dirs.Server, "world/level.dat"), []byte("current-world"), 0600)
	os.WriteFile(filepath.Join(dirs.Server, "world/region/r.0.0.mca"), []byte("current-region"), 0600)
	provider := &coordinatorProvider{flavor: flavor}
	runtime := domain.JavaRuntime{ID: "runtime17", Major: 17, Version: "17-fixture", OS: "linux", Architecture: "x64", Artifact: domain.Artifact{Checksum: domain.Checksum{Algorithm: "sha256", Value: strings.Repeat("a", 64)}}}
	f := &coordinatorFixture{queue: queue, guard: guard, native: native, provider: provider, actor: *actor, dirs: dirs, data: data}
	nativeLifecycle := lifecycle.New(lifecycle.Config{DeferAutostart: func(ctx context.Context, id string) (bool, error) {
		if guard.updates == nil {
			return false, nil
		}
		return guard.updates.StartupBlocked(ctx, id)
	}, DB: database, DataDir: data, Instances: instances, Jobs: queue, Native: native, Runtime: coordinatorRuntime{runtime}, Providers: []domain.DistributionProvider{provider}, VerifyAndSeal: func(context.Context, string, domain.LaunchSpec) error {
		f.sealCount++
		if f.sealHook != nil {
			if err := f.sealHook(); err != nil {
				return err
			}
		}
		if f.sealCount == f.failSeal {
			return errors.New("injected native verification failure")
		}
		return nil
	}})
	plan, _ := provider.Resolve(t.Context(), domain.VersionSelection{Flavor: flavor, LoaderVersion: func() string {
		if flavor == domain.FlavorFabric {
			return "0.16.0"
		}
		return ""
	}(), MinecraftVersion: "1.20.1"})
	spec, _ := provider.Install(t.Context(), "alpha", plan, runtime, io.Discard)
	spec.Args = append([]string{"-Xms512M", "-Xmx1024M"}, spec.Args...)
	raw, _ := json.Marshal(map[string]any{"generation": strings.Repeat("d", 32), "plan": plan, "spec": spec})
	for _, query := range []string{"INSERT INTO lifecycle_installations(instance_id,generation,installation_json) VALUES('alpha',?,?)", "INSERT INTO lifecycle_generations(instance_id,generation,installation_json) VALUES('alpha',?,?)"} {
		if _, err = database.Exec(query, strings.Repeat("d", 32), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = database.Exec("UPDATE instances SET java_runtime_id='runtime17',java_major=17 WHERE id='alpha'"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'"); err != nil {
		t.Fatal(err)
	}
	f.contentVersion = &catalog.Version{}
	contentService, err := content.New(content.Config{DB: database, Paths: paths, Instances: instances, Queue: queue, Catalog: coordinatorCatalog{version: f.contentVersion}, Recovery: guard})
	if err != nil {
		t.Fatal(err)
	}
	packs, err := modpack.New(modpack.Config{DB: database, Paths: paths, Instances: instances, Queue: queue, Catalog: coordinatorCatalog{}, Content: contentService, Recovery: guard})
	if err != nil {
		t.Fatal(err)
	}
	backups := backup.New(backup.Config{DB: database, Paths: paths, Instances: instances, Jobs: queue, Native: native, Worlds: world.New(database, instances, paths, queue)})
	f.s, err = New(Config{DB: database, Paths: paths, Instances: instances, Jobs: queue, Lifecycle: nativeLifecycle, Backups: backups, Content: contentService, Packs: packs, BeginUpdateStop: func(ctx context.Context, e *jobs.Execution, actor domain.User) (int64, lifecycle.RecoveryStartGuard, error) {
		return 1, func(ctx context.Context, id string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
			return start()
		}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	guard.updates = f.s
	if err = f.s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { queue.Stop(context.Background()) })
	return f
}
func (f *coordinatorFixture) wait(t *testing.T, job jobs.Job) jobs.Job {
	t.Helper()
	return f.waitWithin(t, job, 3*time.Second)
}
func (f *coordinatorFixture) waitWithin(t *testing.T, job jobs.Job, budget time.Duration) jobs.Job {
	t.Helper()
	started := time.Now()
	ctx, cancel := context.WithTimeout(t.Context(), budget)
	defer cancel()
	subscription, err := f.queue.Subscribe(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	events := subscription.Events
	// Events wake normal observations. A bounded fallback also observes durable
	// state after a subscriber gap without contending for SQLite every millisecond.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var last jobs.Job
	transaction, transactionState, previous := "", "", ""
	diagnostic := func() string {
		return "job=" + job.ID + " kind=" + job.Kind + " state=" + string(last.State) + " phase=" + last.Phase + " transaction=" + transaction + " transaction_state=" + transactionState
	}
	for {
		current, err := f.queue.Get(ctx, job.ID)
		if err != nil {
			t.Fatalf("coordinator observation failed after %s: %s: %v", time.Since(started), diagnostic(), err)
		}
		last = current
		err = f.s.config.DB.QueryRowContext(ctx, "SELECT id,state FROM update_transactions WHERE job_id=?", job.ID).Scan(&transaction, &transactionState)
		if errors.Is(err, sql.ErrNoRows) {
			transaction, transactionState = "none", "none"
		} else if err != nil {
			t.Fatalf("coordinator transaction observation failed after %s: %s: %v", time.Since(started), diagnostic(), err)
		}
		if observed := diagnostic(); observed != previous {
			t.Logf("coordinator after %s: %s", time.Since(started), observed)
			previous = observed
		}
		if current.State != domain.JobRunning && current.State != domain.JobQueued {
			t.Logf("coordinator terminal state observed after %s (budget %s)", time.Since(started), budget)
			return current
		}
		select {
		case <-ctx.Done():
			t.Fatalf("coordinator job timed out after %s (budget %s): %s: %v", time.Since(started), budget, diagnostic(), ctx.Err())
		case _, open := <-events:
			if !open {
				events = nil
			}
		case <-tick.C:
		}
	}
}
func (f *coordinatorFixture) preview(t *testing.T) Plan {
	t.Helper()
	selection := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.2"}
	job, err := f.s.Preview(t.Context(), "alpha", Request{Mode: Manual, Selection: &selection}, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		t.Fatalf("preview failed %+v", done)
	}
	plans, err := f.s.ListPreviews(t.Context(), "alpha", f.actor)
	if err != nil || len(plans) != 1 {
		t.Fatal(plans, err)
	}
	return plans[0]
}
func TestCoordinatorStagesWithoutMutationsThenCommitsOnePointerTransaction(t *testing.T) {
	f := newCoordinatorFixture(t)
	before, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	plan := f.preview(t)
	if len(plan.Blockers) > 0 {
		t.Fatal(plan.Blockers)
	}
	afterPreview, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if before.Generation != afterPreview.Generation || afterPreview.Plan.Selection.MinecraftVersion != "1.20.1" || f.native.starts != 0 {
		t.Fatal("preview mutated live selection/process")
	}
	var count int
	f.s.config.DB.QueryRow("SELECT count(*) FROM backups").Scan(&count)
	if count != 0 {
		t.Fatal("preview captured backup")
	}
	job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	done := f.wait(t, job)
	if done.State != domain.JobSucceeded {
		t.Fatalf("apply failed %+v", done)
	}
	current, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if err != nil || current.Plan.Selection.MinecraftVersion != "1.20.2" || current.Generation == before.Generation {
		t.Fatal(current, err)
	}
	b, _ := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
	if string(b) != "current-world" {
		t.Fatal("world was changed")
	}
	f.s.config.DB.QueryRow("SELECT (SELECT count(*) FROM lifecycle_activations)+(SELECT count(*) FROM content_journals)+(SELECT count(*) FROM modpack_journals)").Scan(&count)
	if count != 0 {
		t.Fatal("ordinary journal minted", count)
	}
	if err = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorChangedMetadataAndBackupFailureBlockBeforeStop(t *testing.T) {
	for _, failure := range []string{"metadata", "backup"} {
		t.Run(failure, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			plan := f.preview(t)
			if failure == "metadata" {
				f.provider.mu.Lock()
				f.provider.changed = true
				f.provider.mu.Unlock()
			} else {
				os.RemoveAll(filepath.Join(f.dirs.Server, "world"))
				os.Mkdir(filepath.Join(f.dirs.Server, "world"), 0700)
				os.Symlink("/tmp", filepath.Join(f.dirs.Server, "world", "region"))
			}
			job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			done := f.wait(t, job)
			if done.State == domain.JobSucceeded {
				t.Fatal("unsafe update succeeded")
			}
			var count int
			f.s.config.DB.QueryRow("SELECT count(*) FROM update_transactions").Scan(&count)
			if count != 0 {
				t.Fatal("failed prerequisites admitted transaction")
			}
			current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
			if current.Plan.Selection.MinecraftVersion != "1.20.1" || f.native.starts != 0 {
				t.Fatal("prerequisite failure changed live state")
			}
		})
	}
}

func TestCoordinatorSealFailureRestoresOnlyPrebootExecutableState(t *testing.T) {
	f := newCoordinatorFixture(t)
	plan := f.preview(t)
	f.failSeal = 1
	job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State == domain.JobSucceeded {
		t.Fatal("seal failure reported success")
	}
	var state string
	if err = f.s.config.DB.QueryRow("SELECT state FROM update_transactions").Scan(&state); err != nil || state != "rolled_back" {
		t.Fatal(state, err)
	}
	current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if current.Plan.Selection.MinecraftVersion != "1.20.1" {
		t.Fatal("failed preboot seal changed selection")
	}
	if err = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); err != nil {
		t.Fatal("preboot recovery left blocker", err)
	}
}
func (f *coordinatorFixture) pauseRecovery(t *testing.T) {
	t.Helper()
	if err := f.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement := jobs.New(f.s.config.DB, nil)
	f.guard.queue = replacement
	if err := replacement.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { replacement.Stop(context.Background()) })
}
func TestCoordinatorCommittedInterruptionRecoversBeforeOrdinaryMaterialization(t *testing.T) {
	f := newCoordinatorFixture(t)
	plan := f.preview(t)
	f.failSeal = 2
	job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State == domain.JobSucceeded {
		t.Fatal("injected postcommit seal succeeded")
	}
	current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if current.Plan.Selection.MinecraftVersion != "1.20.2" {
		t.Fatal("committed NEW selection lost")
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "server.properties"), []byte("motd=crash-intermediate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.pauseRecovery(t)
	if err = f.s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	f.s.config.DB.QueryRow("SELECT state FROM update_transactions").Scan(&state)
	if state != "complete" {
		t.Fatal(state)
	}
	raw, _ := os.ReadFile(filepath.Join(f.dirs.Server, "server.properties"))
	if strings.Contains(string(raw), "crash-intermediate") {
		t.Fatal("managed config not recovered")
	}
	if err = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorBootIntentCrashReconstructsWorldRiskWithoutOldPointers(t *testing.T) {
	f := newCoordinatorFixture(t)
	plan := f.preview(t)
	job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	// This setup performs a real safety backup and activation before injecting
	// the boot-intent crash. It has no product three-second performance budget.
	if done := f.waitWithin(t, job, 30*time.Second); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	var id, raw string
	if err = f.s.config.DB.QueryRow("SELECT id,journal_json FROM update_transactions").Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	j, err := decodeJournal(raw, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	j.BootAttempted = true
	if err = f.s.setJournalState(t.Context(), id, j, "boot_attempted"); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after durable intent and both publication-marker removals,
	// before any native start or risk-marker publication.
	f.pauseRecovery(t)
	if err = f.s.Reconcile(t.Context()); err != nil {
		t.Fatal("safe administrator hold prevents manager startup", err)
	}
	if blocked, err := f.s.StartupBlocked(t.Context(), "alpha"); err != nil || !blocked {
		t.Fatal("world-risk hold not recognized", blocked, err)
	}
	if err = f.s.CheckReady(t.Context(), "alpha"); err == nil {
		t.Fatal("ordinary admission bypassed world-risk hold")
	}
	risk, present, err := launcher.ReadWorldRiskBlocker(t.Context(), f.data, "alpha")
	if err != nil || !present || risk.TransactionID != id {
		t.Fatal(risk, present, err)
	}
	current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if current.Plan.Selection.MinecraftVersion != "1.20.2" || f.native.starts != 0 {
		t.Fatal("boot recovery changed NEW pointers or restarted")
	}
	b, _ := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
	if string(b) != "current-world" {
		t.Fatal("boot recovery changed world")
	}
}
func TestAutomaticAdapterPinsMinecraftAndReturnsExactIdempotentJob(t *testing.T) {
	f := newCoordinatorFixture(t)
	f.provider.mu.Lock()
	f.provider.changed = true
	f.provider.mu.Unlock()
	job, err := f.s.EnqueueAutomaticOnce(t.Context(), "schedule:alpha:1", "alpha", f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	replay, err := f.s.EnqueueAutomaticOnce(t.Context(), "schedule:alpha:1", "alpha", f.actor)
	if err != nil || replay.ID != job.ID {
		t.Fatal(replay, err)
	}
	current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if current.Plan.Selection.MinecraftVersion != "1.20.1" || current.Plan.Selection.Flavor != domain.FlavorVanilla {
		t.Fatal("automatic update crossed pin")
	}
	var count int
	f.s.config.DB.QueryRow("SELECT count(*) FROM update_transactions").Scan(&count)
	if count != 1 {
		t.Fatal("idempotent replay repeated transaction")
	}
}

func TestAutostartSuspensionOrderingAndUncertainFailures(t *testing.T) {
	for _, failure := range []string{"none", "disable", "restore"} {
		t.Run(failure, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			if _, err := f.s.config.DB.Exec("INSERT INTO lifecycle_autostart(instance_id,desired,applied)VALUES('alpha',1,1)"); err != nil {
				t.Fatal(err)
			}
			plan := f.preview(t)
			f.native.mu.Lock()
			if failure == "disable" {
				f.native.failPolicyAt = 1
			}
			if failure == "restore" {
				f.native.failPolicyAt = 2
			}
			f.native.policyHook = func(index int, enabled bool) error {
				if index == 1 {
					var state, raw string
					if err := f.s.config.DB.QueryRow("SELECT state,journal_json FROM update_transactions").Scan(&state, &raw); err != nil {
						return err
					}
					var j Journal
					if json.Unmarshal([]byte(raw), &j) != nil || j.Autostart == nil || j.Autostart.PriorApplied == nil || !*j.Autostart.PriorApplied || j.Autostart.DisableConfirmed || j.BootAttempted || state != "prepared" {
						return errors.New("disable preceded durable exact policy preimage")
					}
					marker, present, err := launcher.ReadUpdateBlocker(t.Context(), f.data, "alpha")
					if err != nil || !present || marker.TransactionID == "" {
						return errors.New("disable preceded canonical update marker")
					}
					pending, err := launcher.UpdateActivationPending(t.Context(), f.data, "alpha")
					if err != nil || !pending {
						return errors.New("disable preceded protected activation marker")
					}
				}
				if index == 2 {
					current, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
					if err != nil || current.Plan.Selection.MinecraftVersion != "1.20.2" {
						return errors.New("policy restoration preceded new pointer finalization")
					}
				}
				return nil
			}
			f.native.mu.Unlock()
			job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			done := f.wait(t, job)
			var applied sql.NullBool
			var desired bool
			var policyError string
			if err = f.s.config.DB.QueryRow("SELECT desired,applied,error FROM lifecycle_autostart WHERE instance_id='alpha'").Scan(&desired, &applied, &policyError); err != nil {
				t.Fatal(err)
			}
			if !desired {
				t.Fatal("administrator desired policy overwritten")
			}
			if failure == "none" {
				if done.State != domain.JobSucceeded || !applied.Valid || !applied.Bool || policyError != "" {
					t.Fatal(done, applied, policyError)
				}
			} else {
				if applied.Valid || policyError == "" {
					t.Fatal("uncertain native policy fabricated", applied, policyError)
				}
				if failure == "disable" {
					if done.State == domain.JobSucceeded {
						t.Fatal("failed disable allowed update")
					}
					current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
					if current.Plan.Selection.MinecraftVersion != "1.20.1" {
						t.Fatal("failed suspension allowed selection change")
					}
					if err = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); err == nil {
						t.Fatal("failed suspension released boot blocker")
					}
				} else if done.State != domain.JobSucceeded {
					t.Fatal("verified pointer result lost to policy-only partial failure", done)
				}
			}
		})
	}
}

func TestBootRiskStartupKeepsRunningBytesAndAllowsExplicitRollbackJob(t *testing.T) {
	for _, state := range []string{"boot_attempted", "complete"} {
		t.Run(state, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			plan := f.preview(t)
			job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			// This composes a durable full backup and file/SQL activation; three
			// seconds is not a product deadline. Keep the larger bound local here.
			if done := f.waitWithin(t, job, 30*time.Second); done.State != domain.JobSucceeded {
				t.Fatal(done)
			}
			var transaction, raw string
			f.s.config.DB.QueryRow("SELECT id,journal_json FROM update_transactions").Scan(&transaction, &raw)
			j, err := decodeJournal(raw, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			j.BootAttempted = true
			j.StartedIdentity = nil
			if err = f.s.setJournalState(t.Context(), transaction, j, state); err != nil {
				t.Fatal(err)
			}
			if state == "complete" {
				marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: transaction, JobID: job.ID, Kind: "update"}
				if err = launcher.WriteUpdateBlocker(t.Context(), f.data, marker); err != nil {
					t.Fatal(err)
				}
				if err = launcher.WriteWorldRiskBlocker(t.Context(), f.data, marker); err != nil {
					t.Fatal(err)
				}
				if err = launcher.MarkActivationPending(t.Context(), f.data, "alpha"); err != nil {
					t.Fatal(err)
				}
			}
			// The JVM may have survived manager death; no exact returned identity was
			// retained. Runtime/cwd similarities must never authorize a signal.
			f.native.mu.Lock()
			f.native.running = true
			f.native.mu.Unlock()
			currentConfig := []byte("motd=game-modified-after-start\n")
			os.WriteFile(filepath.Join(f.dirs.Server, "server.properties"), currentConfig, 0600)
			originalGeneration := j.After.Generation
			if err = f.queue.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			replacement := jobs.New(f.s.config.DB, nil)
			config := f.s.config
			config.Jobs = replacement
			config.Rollbacks = nil
			next, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := NewRollbackCoordinator(next)
			if err != nil {
				t.Fatal(err)
			}
			observed := make(chan error, 1)
			if err = next.SetRollbackCoordinator(observingRollback{RollbackCoordinator: coordinator, result: observed}); err != nil {
				t.Fatal(err)
			}
			if err = next.Register(); err != nil {
				t.Fatal(err)
			}
			f.s = next
			f.queue = replacement
			f.guard.queue = replacement
			f.guard.updates = next
			if err = replacement.StartPaused(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer replacement.Stop(context.Background())
			if err = next.Reconcile(t.Context()); err != nil {
				t.Fatal("safe running hold hid administration", err)
			}
			if hold, err := next.StartupBlocked(t.Context(), "alpha"); err != nil || !hold {
				t.Fatal(hold, err)
			}
			result, err := next.config.Instances.Reconcile(t.Context())
			if err != nil || len(result.Deferred) != 1 {
				t.Fatal(result, err)
			}
			if err = next.config.Lifecycle.VerifyRunningAfterReconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			plans, err := next.config.Lifecycle.AutostartPlans(t.Context())
			if err != nil || len(plans) != 0 {
				t.Fatal(plans, err)
			}
			after, _ := next.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
			bytes, _ := os.ReadFile(filepath.Join(f.dirs.Server, "server.properties"))
			f.native.mu.Lock()
			running := f.native.running
			f.native.mu.Unlock()
			if after.Generation != originalGeneration || string(bytes) != string(currentConfig) || !running {
				t.Fatal("startup rewrote held generation/configuration or signaled uncertain JVM")
			}
			if err = f.native.Stop(t.Context(), domain.ProcessIdentity{InstanceID: "alpha", PID: 55}); err != nil {
				t.Fatal(err)
			} // explicit operator stop fixture
			rollback, err := next.PreviewRollback(t.Context(), "alpha", transaction, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			queued, err := next.EnqueueRollback(t.Context(), "alpha", rollback.ID, "alpha", true, f.actor)
			if err != nil {
				t.Fatal("confirmed recovery unavailable after restart", err)
			}
			if err = replacement.Resume(); err != nil {
				t.Fatal(err)
			}
			if done := f.waitWithin(t, queued, 30*time.Second); done.State != domain.JobSucceeded {
				t.Fatalf("separately confirmed stopped rollback failed: %+v; cause: %v", done, <-observed)
			}
			after, err = next.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
			if err != nil || after.Plan.Selection.MinecraftVersion != "1.20.1" {
				t.Fatal(after, err)
			}
			if f.native.starts != 0 {
				t.Fatal("rollback restarted game")
			}
		})
	}
}

type observingRollback struct {
	RollbackCoordinator
	result chan error
}

func (o observingRollback) Run(ctx context.Context, e *jobs.Execution) error {
	err := o.RollbackCoordinator.Run(ctx, e)
	o.result <- err
	return err
}

func TestCoordinatorTerminalDiagnosticFailureCannotUndoCompletion(t *testing.T) {
	f := newCoordinatorFixture(t)
	plan := f.preview(t)
	if _, err := f.s.config.DB.Exec(`CREATE TRIGGER fail_final_update_log BEFORE INSERT ON job_logs WHEN NEW.message LIKE 'Update committed with verified%' BEGIN SELECT RAISE(FAIL,'injected final diagnostic failure'); END`); err != nil {
		t.Fatal(err)
	}
	job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		t.Fatalf("durable completion became failure after diagnostic: %+v", done)
	}
	var state string
	if err = f.s.config.DB.QueryRow("SELECT state FROM update_transactions").Scan(&state); err != nil || state != "complete" {
		t.Fatal(state, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); err != nil {
		t.Fatal("diagnostic recreated publication blockers", err)
	}
}

func TestCompleteBootMarkersPreserveCurrentBytesDuringComposedStartup(t *testing.T) {
	for _, tc := range []struct {
		name              string
		running, riskOnly bool
	}{
		{name: "surviving JVM", running: true},
		{name: "stopped game changed configuration"},
		{name: "risk marker only", riskOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCoordinatorFixture(t)
			plan := f.preview(t)
			job, err := f.s.EnqueueApply(t.Context(), "alpha", plan.ID, true, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			if done := f.wait(t, job); done.State != domain.JobSucceeded {
				t.Fatal(done)
			}
			var transaction, raw string
			if err = f.s.config.DB.QueryRow("SELECT id,journal_json FROM update_transactions").Scan(&transaction, &raw); err != nil {
				t.Fatal(err)
			}
			j, err := decodeJournal(raw, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			j.BootAttempted = true
			j.StartedIdentity = nil // no exact signal authority survived
			if err = f.s.setJournalState(t.Context(), transaction, j, "complete"); err != nil {
				t.Fatal(err)
			}
			marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: transaction, JobID: job.ID, Kind: "update"}
			if !tc.riskOnly {
				if err = launcher.WriteUpdateBlocker(t.Context(), f.data, marker); err != nil {
					t.Fatal(err)
				}
			}
			if err = launcher.WriteWorldRiskBlocker(t.Context(), f.data, marker); err != nil {
				t.Fatal(err)
			}
			if err = launcher.MarkActivationPending(t.Context(), f.data, "alpha"); err != nil {
				t.Fatal(err)
			}
			f.native.mu.Lock()
			f.native.running = tc.running
			f.native.mu.Unlock()
			currentBytes := []byte("motd=changed-by-game-after-boot\n")
			if err = os.WriteFile(filepath.Join(f.dirs.Server, "server.properties"), currentBytes, 0600); err != nil {
				t.Fatal(err)
			}
			seals := f.sealCount
			f.pauseRecovery(t)
			if err = f.s.Reconcile(t.Context()); err != nil {
				t.Fatal("safe terminal boot hold hid administration", err)
			}
			if hold, err := f.s.StartupBlocked(t.Context(), "alpha"); err != nil || !hold {
				t.Fatal("terminal boot markers did not become administrative hold", hold, err)
			}
			result, err := f.s.config.Instances.Reconcile(t.Context())
			if err != nil || len(result.Deferred) != 1 {
				t.Fatal(result, err)
			}
			if err = f.s.config.Lifecycle.VerifyRunningAfterReconcile(t.Context()); err != nil {
				t.Fatal(err)
			}
			starts, err := f.s.config.Lifecycle.AutostartPlans(t.Context())
			if err != nil || len(starts) != 0 {
				t.Fatal(starts, err)
			}
			after, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
			if err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(f.dirs.Server, "server.properties"))
			if err != nil {
				t.Fatal(err)
			}
			f.native.mu.Lock()
			running := f.native.running
			f.native.mu.Unlock()
			if after.Generation != j.After.Generation || string(actual) != string(currentBytes) || running != tc.running || f.sealCount != seals {
				t.Fatal("postboot recovery mutated bytes/pointers, resealed, or signaled uncertain JVM")
			}
			if err = f.s.CheckReady(t.Context(), "alpha"); err == nil {
				t.Fatal("terminal SQL bypassed retained world-risk admission")
			}
		})
	}
}
