package update

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRollbackCoordinatorRequiresCompleteService(t *testing.T) {
	for _, s := range []*Service{nil, {}} {
		if _, err := NewRollbackCoordinator(s); err == nil {
			t.Fatal("incomplete rollback coordinator accepted")
		}
	}
}

// Native verification is an explicitly labelled fixture. All queue, stopped
// scope, safety archive, world move, SQL, provenance and role paths are real.
type rollbackNativeFixture struct{ starts int }

func (n *rollbackNativeFixture) WithStopped(_ context.Context, _ string, fn func() error) error {
	return fn()
}
func (n *rollbackNativeFixture) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	return domain.ProcessIdentity{}, os.ErrNotExist
}
func (n *rollbackNativeFixture) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{State: domain.StateStopped}, nil
}
func (n *rollbackNativeFixture) Start(context.Context, string, domain.LaunchSpec) (domain.ProcessIdentity, error) {
	n.starts++
	return domain.ProcessIdentity{}, errors.New("rollback must not start a game")
}
func (n *rollbackNativeFixture) Stop(context.Context, domain.ProcessIdentity) error {
	return errors.New("rollback must already be stopped")
}
func (n *rollbackNativeFixture) SetAutostart(context.Context, string, bool) error { return nil }
func (n *rollbackNativeFixture) ArmSaveLease(context.Context, domain.ProcessIdentity, string, time.Time) error {
	return errors.New("stopped fixture cannot arm save lease")
}

type rollbackGuardFixture struct {
	queue  *jobs.Service
	native *rollbackNativeFixture
}

func (g rollbackGuardFixture) WithStopped(ctx context.Context, id string, fn func() error) error {
	return g.native.WithStopped(ctx, id, fn)
}
func (g rollbackGuardFixture) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { return g.native.WithStopped(ctx, id, func() error { return fn(id) }) })
}
func (g rollbackGuardFixture) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.queue.WithPausedInstance(ctx, id, func() error { return g.native.WithStopped(ctx, id, fn) })
}

type rollbackRuntimeFixture struct{ failID string }

func (r *rollbackRuntimeFixture) Get(_ context.Context, id string) (domain.JavaRuntime, error) {
	if r.failID == id {
		return domain.JavaRuntime{}, errors.New("fixture historical managed runtime verification failed")
	}
	major := 17
	if id == "r8" {
		major = 8
	}
	return domain.JavaRuntime{ID: id, Major: major}, nil
}
func (*rollbackRuntimeFixture) EnsureRequirements(context.Context, domain.LaunchRequirements, io.Writer) (domain.JavaRuntime, error) {
	return domain.JavaRuntime{}, errors.New("rollback cannot download Java")
}

type rollbackCatalogFixture struct{}

func (rollbackCatalogFixture) Project(context.Context, catalog.Provider, string) (catalog.Project, error) {
	return catalog.Project{}, errors.New("rollback cannot resolve catalog")
}
func (rollbackCatalogFixture) Version(context.Context, catalog.Provider, string, string) (catalog.Version, error) {
	return catalog.Version{}, errors.New("rollback cannot resolve catalog")
}
func (rollbackCatalogFixture) Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error) {
	return catalog.VerifiedArtifact{}, errors.New("rollback cannot download content")
}

type rollbackFixture struct {
	s        *Service
	c        *rollbackCoordinator
	database *sql.DB
	q        *jobs.Service
	native   *rollbackNativeFixture
	dirs     instance.Directories
	actor    domain.User
	source   rollbackSource
	parent   string
	backup   domain.Backup
	seal     func(context.Context, string, domain.LaunchSpec) error
	runtime  *rollbackRuntimeFixture
}

func newRollbackFixture(t *testing.T) *rollbackFixture {
	t.Helper()
	ctx := t.Context()
	database, err := db.Open(ctx, filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	data := t.TempDir()
	paths, err := instance.NewPaths(data)
	if err != nil {
		t.Fatal(err)
	}
	f := &rollbackFixture{database: database, native: &rollbackNativeFixture{}, actor: domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}, parent: strings.Repeat("a", 32)}
	if _, err = database.ExecContext(ctx, "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'other-admin','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(database, nil)
	f.q = q
	guard := rollbackGuardFixture{q, f.native}
	instances := instance.New(database, paths, f.native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	if _, err = instances.Create(ctx, f.actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	f.dirs, err = paths.For("alpha")
	if err != nil {
		t.Fatal(err)
	}
	old := lifecycle.UpdateState{Generation: strings.Repeat("b", 32), Plan: domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5"}, Requirements: domain.LaunchRequirements{JavaMajor: 8, MinJavaMajor: 8}}, Spec: domain.LaunchSpec{JavaRuntimeID: "r8", Args: []string{"-jar", "fixture.jar", "nogui"}}, Runtime: domain.JavaRuntime{ID: "r8", Major: 8}}
	if err = os.MkdirAll(filepath.Join(f.dirs.Server, "world/region"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "world/level.dat"), []byte("historical world"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "world/region/r.0.0.mca"), []byte("historical region"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(f.dirs.Server, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "config/local.toml"), []byte("historical customization"), 0600); err != nil {
		t.Fatal(err)
	}
	// Preserve an inert unknown disabled artifact as unknown historical content.
	var jar bytes.Buffer
	zw := zip.NewWriter(&jar)
	entry, _ := zw.Create("unrecognized.txt")
	_, _ = entry.Write([]byte("inert"))
	_ = zw.Close()
	if err = os.MkdirAll(filepath.Join(f.dirs.Server, "mods"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "mods/local.jar.disabled"), jar.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	descriptor, err := content.InspectPackReader(ctx, bytes.NewReader(jar.Bytes()), int64(jar.Len()))
	if err != nil {
		t.Fatal(err)
	}
	item := content.Installed{ID: "local", Kind: catalog.Mod, Name: "Local unknown", Unknown: true, Descriptor: descriptor, SHA256: rollbackDigest(jar.Bytes()), Files: []content.OwnedFile{{Path: "mods/local.jar.disabled", SHA256: rollbackDigest(jar.Bytes())}}}
	itemRaw, _ := json.Marshal(item)
	if _, err = database.ExecContext(ctx, "INSERT INTO content_items(instance_id,id,manifest_json) VALUES('alpha','local',?)", string(itemRaw)); err != nil {
		t.Fatal(err)
	}
	baseline := &modpack.Baseline{Revision: "old-baseline", Files: map[string]string{"config/local.toml": strings.Repeat("f", 64)}, Selections: map[string]modpack.Selection{"config/local.toml": {Pinned: true}}, Content: map[string]string{"local": "upstream"}}
	baselineRaw, _ := json.Marshal(baseline)
	if _, err = database.ExecContext(ctx, "INSERT INTO modpack_baselines(instance_id,baseline_json) VALUES('alpha',?)", string(baselineRaw)); err != nil {
		t.Fatal(err)
	}
	installationRaw := func(state lifecycle.UpdateState) string {
		raw, _ := json.Marshal(map[string]any{"generation": state.Generation, "plan": state.Plan, "spec": state.Spec})
		return string(raw)
	}
	if _, err = database.ExecContext(ctx, "INSERT INTO lifecycle_generations(instance_id,generation,installation_json) VALUES('alpha',?,?)", old.Generation, installationRaw(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "INSERT INTO lifecycle_installations(instance_id,generation,installation_json) VALUES('alpha',?,?)", old.Generation, installationRaw(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE instances SET java_runtime_id='r8' WHERE id='alpha'"); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'"); err != nil {
		t.Fatal(err)
	}
	cs, err := content.New(content.Config{DB: database, Paths: paths, Instances: instances, Queue: q, Recovery: guard})
	if err != nil {
		t.Fatal(err)
	}
	packs, err := modpack.New(modpack.Config{DB: database, Paths: paths, Instances: instances, Queue: q, Catalog: rollbackCatalogFixture{}, Content: cs, Recovery: guard})
	if err != nil {
		t.Fatal(err)
	}
	f.runtime = &rollbackRuntimeFixture{}
	ls := lifecycle.New(lifecycle.Config{DB: database, DataDir: data, Instances: instances, Jobs: q, Native: f.native, Runtime: f.runtime, VerifyAndSeal: func(ctx context.Context, id string, spec domain.LaunchSpec) error {
		if f.seal != nil {
			return f.seal(ctx, id, spec)
		}
		return nil
	}})
	backups := backup.New(backup.Config{DB: database, Paths: paths, Instances: instances, Jobs: q, Native: f.native, Worlds: world.New(database, instances, paths, q)})
	if err = backups.Register(); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DB: database, Paths: paths, Instances: instances, Jobs: q, Lifecycle: ls, Backups: backups, Content: cs, Packs: packs, BeginUpdateStop: func(context.Context, *jobs.Execution, domain.User) (int64, lifecycle.RecoveryStartGuard, error) {
		return 0, nil, errors.New("rollback cannot stop")
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.s = s
	adapter, err := NewRollbackCoordinator(s)
	if err != nil {
		t.Fatal(err)
	}
	f.c = adapter.(*rollbackCoordinator)
	if err = s.SetRollbackCoordinator(adapter); err != nil {
		t.Fatal(err)
	}
	if err = s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop(context.Background()) })
	old, err = ls.UpdateSnapshot(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := cs.Snapshot(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	job, err := backups.Enqueue(ctx, "alpha", domain.BackupFull, domain.BackupManual, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := awaitRollbackJob(t, q, job.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	records, err := backups.List(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.JobID == job.ID {
			f.backup = record.Backup
		}
	}
	if f.backup.ID == "" {
		t.Fatal("fixture historical archive missing")
	}
	next := old
	next.Generation = strings.Repeat("c", 32)
	next.Plan = domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.18.2"}, Requirements: domain.LaunchRequirements{JavaMajor: 17, MinJavaMajor: 17}}
	next.Runtime = domain.JavaRuntime{ID: "r17", Major: 17}
	next.Spec = domain.LaunchSpec{JavaRuntimeID: "r17", Args: []string{"-jar", "new-fixture.jar", "nogui"}}
	if _, err = database.ExecContext(ctx, "INSERT INTO lifecycle_generations(instance_id,generation,installation_json) VALUES('alpha',?,?)", next.Generation, installationRaw(next)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE lifecycle_installations SET generation=?,installation_json=? WHERE instance_id='alpha'", next.Generation, installationRaw(next)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.ExecContext(ctx, "UPDATE instances SET minecraft_version='1.18.2',java_major=17,java_runtime_id='r17',updated_at=? WHERE id='alpha'", stamp()); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "world/level.dat"), []byte("current migrated world"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(f.dirs.Server, "config/local.toml"), []byte("new customization"), 0600); err != nil {
		t.Fatal(err)
	}
	next.Revision = stamp()
	afterBaseline := *baseline
	afterBaseline.Revision = "new-baseline"
	afterBaseline.Selections = map[string]modpack.Selection{"config/local.toml": {}}
	updatedBaselineRaw, _ := json.Marshal(afterBaseline)
	if _, err = database.ExecContext(ctx, "UPDATE modpack_baselines SET baseline_json=? WHERE instance_id='alpha'", string(updatedBaselineRaw)); err != nil {
		t.Fatal(err)
	}
	updatedItem := item
	updatedItem.Pinned = true
	updatedItem.Version = "new metadata"
	updatedItemRaw, _ := json.Marshal(updatedItem)
	if _, err = database.ExecContext(ctx, "UPDATE content_items SET manifest_json=? WHERE instance_id='alpha'", string(updatedItemRaw)); err != nil {
		t.Fatal(err)
	}
	f.source = rollbackSource{Version: 1, Plan: Plan{InstanceID: "alpha", From: old.Plan.Selection, Target: next.Plan.Selection, Snapshot: Snapshot{InstanceRevision: old.Revision, Generation: old.Generation, ContentRevision: manifest.Revision, PackRevision: baseline.Revision}}, Before: old, After: next, ContentBefore: manifest, ContentAfter: []content.Installed{updatedItem}, PackBefore: baseline, PackAfter: &afterBaseline, BootAttempted: true}
	raw, _ := json.Marshal(f.source)
	parentJob := strings.Repeat("d", 32)
	if _, err = database.ExecContext(ctx, "INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,'alpha','preview',?,1,'update','boot_attempted',?,?,?)", f.parent, parentJob, f.backup.ID, string(raw), stamp()); err != nil {
		t.Fatal(err)
	}
	marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: f.parent, JobID: parentJob, Kind: "update"}
	if err = launcher.WriteUpdateBlocker(ctx, data, marker); err != nil {
		t.Fatal(err)
	}
	if err = launcher.WriteWorldRiskBlocker(ctx, data, marker); err != nil {
		t.Fatal(err)
	}
	return f
}
func awaitRollbackJob(t *testing.T, q *jobs.Service, id string) jobs.Job {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); {
		job, err := q.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != domain.JobRunning && job.State != domain.JobQueued {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("rollback job timeout")
	return jobs.Job{}
}

func TestExplicitRollbackRestoresWorldAndHistoricalPointersWithoutRestart(t *testing.T) {
	f := newRollbackFixture(t)
	preview, err := f.c.Preview(t.Context(), "alpha", f.parent, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.c.Enqueue(t.Context(), "alpha", preview.ID, "alpha", true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := awaitRollbackJob(t, f.q, job.ID); done.State != domain.JobSucceeded {
		t.Fatalf("rollback failed: %+v", done)
	}
	restored, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
	if err != nil || string(restored) != "historical world" {
		t.Fatalf("world=%q err=%v", restored, err)
	}
	local, err := os.ReadFile(filepath.Join(f.dirs.Server, "config/local.toml"))
	if err != nil || string(local) != "historical customization" {
		t.Fatalf("customization=%q err=%v", local, err)
	}
	state, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan.Selection != f.source.Before.Plan.Selection || state.Generation == f.source.Before.Generation || state.Runtime.ID != "r8" {
		t.Fatalf("historical pointers=%+v", state)
	}
	current, err := f.s.config.Content.Snapshot(t.Context(), "alpha")
	if err != nil || len(current.Items) != 1 || !current.Items[0].Unknown || current.Items[0].Pinned || current.Items[0].Version != "" {
		t.Fatalf("unknown provenance lost: %+v %v", current, err)
	}
	pack, err := f.s.config.Packs.Snapshot(t.Context(), "alpha")
	if err != nil || pack.Baseline.Revision != "old-baseline" || !pack.Baseline.Selections["config/local.toml"].Pinned {
		t.Fatalf("baseline=%+v %v", pack, err)
	}
	if f.native.starts != 0 {
		t.Fatal("rollback restarted game")
	}
	if err = f.s.CheckReady(t.Context(), "alpha"); err != nil {
		t.Fatal("completed rollback left blockers", err)
	}
	if err = launcher.CheckActivationReady(t.Context(), pathsDataDir(f.s.config.Paths, "alpha"), "alpha"); err != nil {
		t.Fatal("independent native boot blocker retained after completion", err)
	}
	var count int
	if err = f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM backups WHERE instance_id='alpha' AND state='ready'").Scan(&count); err != nil || count != 2 {
		t.Fatalf("new current safety missing count=%d err=%v", count, err)
	}
}

func (f *rollbackFixture) enqueue(t *testing.T) jobs.Job {
	t.Helper()
	p, err := f.c.Preview(t.Context(), "alpha", f.parent, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	j, err := f.c.Enqueue(t.Context(), "alpha", p.ID, "alpha", true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	return awaitRollbackJob(t, f.q, j.ID)
}
func (f *rollbackFixture) pending(t *testing.T) (string, json.RawMessage) {
	t.Helper()
	var id, raw string
	if err := f.database.QueryRowContext(t.Context(), "SELECT id,journal_json FROM update_transactions WHERE kind='instance_rollback'").Scan(&id, &raw); err != nil {
		t.Fatal(err)
	}
	return id, json.RawMessage(raw)
}
func (f *rollbackFixture) restartPaused(t *testing.T) {
	t.Helper()
	if err := f.q.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(f.database, nil)
	f.q = q
	f.s.config.Jobs = q
	guard := rollbackGuardFixture{q, f.native}
	f.s.config.Instances = instance.New(f.database, f.s.config.Paths, f.native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	if err := q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop(context.Background()) })
}
func TestExplicitRollbackRecoveryFinishesOnlyPersistedConsent(t *testing.T) {
	for _, cut := range []string{"before world moves", "admission with old marker", "after pointer commit", "receipt complete"} {
		t.Run(cut, func(t *testing.T) {
			f := newRollbackFixture(t)
			calls := 0
			switch cut {
			case "receipt complete":
				if _, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER rollback_cut BEFORE UPDATE OF state ON update_transactions WHEN NEW.kind='instance_rollback' AND NEW.state='complete' BEGIN SELECT RAISE(ABORT,'fixture finalization crash'); END`); err != nil {
					t.Fatal(err)
				}
			default:
				f.seal = func(context.Context, string, domain.LaunchSpec) error {
					calls++
					if cut != "after pointer commit" || calls == 3 {
						return errors.New("fixture process interruption")
					}
					return nil
				}
			}
			if done := f.enqueue(t); done.State != domain.JobFailed {
				t.Fatal("expected interruption", done)
			}
			id, raw := f.pending(t)
			if cut == "before world moves" || cut == "admission with old marker" {
				data, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
				if err != nil || string(data) != "current migrated world" {
					t.Fatalf("world rewound before restoration admission: %q %v", data, err)
				}
			}
			if cut == "admission with old marker" {
				// Exact persisted crash state: intent committed, old marker still published.
				marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: f.parent, JobID: strings.Repeat("d", 32), Kind: "update"}
				bytes, _ := json.Marshal(marker)
				if err := os.WriteFile(filepath.Join(f.dirs.State, "update-transaction.json"), bytes, 0600); err != nil {
					t.Fatal(err)
				}
			}
			f.seal = nil
			if cut == "receipt complete" {
				if _, err := f.database.ExecContext(t.Context(), "DROP TRIGGER rollback_cut"); err != nil {
					t.Fatal(err)
				}
			}
			f.restartPaused(t)
			if err := f.c.Reconcile(t.Context(), "alpha", id, raw); err != nil {
				t.Fatal(err)
			}
			if err := f.s.CheckReady(t.Context(), "alpha"); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Reconcile(t.Context(), "alpha", id, raw); err != nil {
				t.Fatal("completed replay not idempotent", err)
			}
			data, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
			if err != nil || string(data) != "historical world" {
				t.Fatalf("recovery world=%q err=%v", data, err)
			}
			if f.native.starts != 0 {
				t.Fatal("recovery restarted game")
			}
		})
	}
}

func TestExplicitRollbackSameTransactionFailurePreservesCurrentPointers(t *testing.T) {
	f := newRollbackFixture(t)
	if _, err := f.database.ExecContext(t.Context(), `CREATE TRIGGER rollback_pointer_abort BEFORE INSERT ON content_items BEGIN SELECT RAISE(ABORT,'fixture pointer commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if done := f.enqueue(t); done.State != domain.JobFailed {
		t.Fatal("expected pointer abort", done)
	}
	var version, generation, receipt string
	if err := f.database.QueryRowContext(t.Context(), "SELECT i.minecraft_version,l.generation,b.state FROM instances i JOIN lifecycle_installations l ON l.instance_id=i.id JOIN backup_update_rollbacks b ON b.instance_id=i.id WHERE i.id='alpha'").Scan(&version, &generation, &receipt); err != nil {
		t.Fatal(err)
	}
	if version != "1.18.2" || generation != f.source.After.Generation || receipt != "data_replaced" {
		t.Fatalf("partial pointer commit version=%s generation=%s receipt=%s", version, generation, receipt)
	}
	if err := f.s.CheckReady(t.Context(), "alpha"); err == nil {
		t.Fatal("uncommitted restored world could boot")
	}
	if err := launcher.CheckActivationReady(t.Context(), pathsDataDir(f.s.config.Paths, "alpha"), "alpha"); err == nil {
		t.Fatal("independent native guard allowed uncommitted restored world")
	}
	if _, err := f.database.ExecContext(t.Context(), "DROP TRIGGER rollback_pointer_abort"); err != nil {
		t.Fatal(err)
	}
	id, raw := f.pending(t)
	f.restartPaused(t)
	if err := f.c.Reconcile(t.Context(), "alpha", id, raw); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitRollbackRejectsSeparateConsentExpiryAndRevokedAdministrator(t *testing.T) {
	for _, variant := range []string{"missing acknowledgement", "wrong confirmation", "expired preview", "operator", "disabled"} {
		t.Run(variant, func(t *testing.T) {
			f := newRollbackFixture(t)
			p, err := f.c.Preview(t.Context(), "alpha", f.parent, f.actor)
			if err != nil {
				t.Fatal(err)
			}
			confirmation := "alpha"
			ack := true
			switch variant {
			case "missing acknowledgement":
				ack = false
			case "wrong confirmation":
				confirmation = "bravo"
			case "expired preview":
				p.ExpiresAt = time.Now().Add(-time.Hour)
				raw, _ := json.Marshal(p)
				_, err = f.database.ExecContext(t.Context(), "UPDATE update_previews SET private_json=?,expires_at=? WHERE id=?", string(raw), p.ExpiresAt.Format(time.RFC3339Nano), p.ID)
			case "operator":
				_, err = f.database.ExecContext(t.Context(), "UPDATE users SET role='operator' WHERE id=1")
			case "disabled":
				_, err = f.database.ExecContext(t.Context(), "UPDATE users SET disabled=1 WHERE id=1")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.c.Enqueue(t.Context(), "alpha", p.ID, confirmation, ack, f.actor); err == nil {
				t.Fatal("unsafe rollback admitted")
			}
			var count int
			if err = f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM backups WHERE instance_id='alpha'").Scan(&count); err != nil || count != 1 {
				t.Fatalf("refusal made safety/mutated data count=%d err=%v", count, err)
			}
		})
	}
}

func TestExplicitRollbackRecoveryRejectsChangedOwnershipBeforeWorldWrites(t *testing.T) {
	for _, variant := range []string{"missing consent", "false consent", "changed parent history", "changed archive metadata", "changed archive bytes", "changed backup manifest", "foreign marker", "revoked admin", "copied journal", "changed original job consent"} {
		t.Run(variant, func(t *testing.T) {
			f := newRollbackFixture(t)
			f.seal = func(context.Context, string, domain.LaunchSpec) error { return errors.New("fixture before world move") }
			if done := f.enqueue(t); done.State != domain.JobFailed {
				t.Fatal(done)
			}
			id, raw := f.pending(t)
			f.seal = nil
			var err error
			switch variant {
			case "missing consent":
				_, err = f.database.ExecContext(t.Context(), "DELETE FROM backup_update_rollbacks")
			case "false consent":
				_, err = f.database.ExecContext(t.Context(), "UPDATE backup_update_rollbacks SET consent_json=json_set(consent_json,'$.world_rewind',json('false'))")
			case "changed parent history":
				_, err = f.database.ExecContext(t.Context(), "UPDATE update_transactions SET journal_json=json_set(journal_json,'$.before.generation',?) WHERE id=?", strings.Repeat("e", 32), f.parent)
			case "changed archive metadata":
				_, err = f.database.ExecContext(t.Context(), "UPDATE backups SET metadata_json=json_set(metadata_json,'$.checksum.value',?) WHERE id=?", strings.Repeat("e", 64), f.backup.ID)
			case "changed archive bytes":
				err = os.WriteFile(filepath.Join(f.dirs.Backups, f.backup.ID+".zip"), []byte("changed after admission"), 0600)
			case "changed backup manifest":
				_, err = f.database.ExecContext(t.Context(), "UPDATE backup_update_rollbacks SET journal_json=json_set(journal_json,'$.manifest.files[0].sha256',?)", strings.Repeat("e", 64))
			case "foreign marker":
				marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: strings.Repeat("e", 32), JobID: strings.Repeat("f", 32), Kind: "instance_rollback"}
				bytes, _ := json.Marshal(marker)
				err = os.WriteFile(filepath.Join(f.dirs.State, "update-transaction.json"), bytes, 0600)
			case "revoked admin":
				_, err = f.database.ExecContext(t.Context(), "UPDATE users SET role='operator' WHERE id=1")
			case "copied journal":
				raw = append(raw, ' ')
			case "changed original job consent":
				_, err = f.database.ExecContext(t.Context(), "UPDATE jobs SET payload=json_set(payload,'$.acknowledge_world_rewind',json('false')) WHERE kind='instance_rollback'")
			}
			if err != nil {
				t.Fatal(err)
			}
			f.restartPaused(t)
			if err = f.c.Reconcile(t.Context(), "alpha", id, raw); err == nil {
				t.Fatal("unsafe ownership recovery accepted")
			}
			data, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
			if err != nil || string(data) != "current migrated world" {
				t.Fatalf("refusal changed world=%q err=%v", data, err)
			}
			if err = f.s.CheckReady(t.Context(), "alpha"); err == nil {
				t.Fatal("refused ownership unblocked boot")
			}
		})
	}
}

func TestExplicitRollbackTerminalMarkerCleanupRevalidatesHistoricalSelection(t *testing.T) {
	f := newRollbackFixture(t)
	if done := f.enqueue(t); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	id, raw := f.pending(t)
	var journal rollbackJournal
	if err := json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	// Simulate interruption after complete SQL and before exact marker deletion.
	marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: id, JobID: journal.Receipt.JobID, Kind: "instance_rollback"}
	if err := launcher.WriteUpdateBlocker(t.Context(), pathsDataDir(f.s.config.Paths, "alpha"), marker); err != nil {
		t.Fatal(err)
	}
	old := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: f.parent, JobID: strings.Repeat("d", 32), Kind: "update"}
	if err := launcher.WriteWorldRiskBlocker(t.Context(), pathsDataDir(f.s.config.Paths, "alpha"), old); err != nil {
		t.Fatal(err)
	}
	f.restartPaused(t)
	if err := f.c.Reconcile(t.Context(), "alpha", id, raw); err != nil {
		t.Fatal(err)
	}
	if err := f.s.CheckReady(t.Context(), "alpha"); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitRollbackSafetyFailureAndMixedLayoutNeverChangeSelectionOrWorld(t *testing.T) {
	for _, cause := range []string{"unsafe local addon", "mixed world layout"} {
		t.Run(cause, func(t *testing.T) {
			f := newRollbackFixture(t)
			// Preparation remains valid: the new CURRENT full capture must detect these
			// unsafe/ambiguous local bytes before any old selection/world mutation.
			if cause == "mixed world layout" {
				if err := os.MkdirAll(filepath.Join(f.dirs.Server, "world/dimensions/minecraft/overworld"), 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(f.dirs.Server, "config/unsafe-link")); err != nil {
					t.Fatal(err)
				}
			}
			if done := f.enqueue(t); done.State != domain.JobFailed {
				t.Fatal("unsafe current safety accepted", done)
			}
			var version string
			var successors int
			if err := f.database.QueryRowContext(t.Context(), "SELECT minecraft_version FROM instances WHERE id='alpha'").Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := f.database.QueryRowContext(t.Context(), "SELECT count(*) FROM update_transactions WHERE kind='instance_rollback'").Scan(&successors); err != nil {
				t.Fatal(err)
			}
			if version != "1.18.2" || successors != 0 {
				t.Fatalf("safety failure changed selection=%s successors=%d", version, successors)
			}
			data, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
			if err != nil || string(data) != "current migrated world" {
				t.Fatalf("safety failure rewound world=%q %v", data, err)
			}
		})
	}
}

func TestRollbackParticipantCopiesExpiryAndForeignTransactionRefused(t *testing.T) {
	f := newRollbackFixture(t)
	f.seal = func(context.Context, string, domain.LaunchSpec) error {
		return errors.New("fixture before restoration")
	}
	if done := f.enqueue(t); done.State != domain.JobFailed {
		t.Fatal(done)
	}
	id, raw := f.pending(t)
	var j rollbackJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	f.seal = nil
	f.restartPaused(t)
	var expiredLifecycle *lifecycle.RollbackParticipant
	var expiredContent *content.RollbackParticipant
	var expiredPack *modpack.RollbackParticipant
	err := f.s.config.Instances.WithStoppedRecovery(t.Context(), "alpha", func(session *instance.StoppedSession) error {
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *instance.TransactionScope) error {
			return f.s.config.Lifecycle.WithRollbackParticipant(ctx, scope, id, j.Preview.Snapshot.Generation, f.source.Before, f.actor, func(target *lifecycle.RollbackParticipant) error {
				expiredLifecycle = target
				copyTarget := copiedRollbackCapability(target)
				if _, err := copyTarget.State(); domain.AsError(err).Code != domain.CodeForbidden {
					return errors.New("copied historical target accepted")
				}
				if err := target.Seal(ctx); err != nil {
					return err
				}
				return f.s.config.Packs.WithRollbackParticipant(ctx, scope, j.CurrentPack, f.source.PackBefore, f.actor, func(pack *modpack.RollbackParticipant) error {
					expiredPack = pack
					copyPack := copiedRollbackCapability(pack)
					if err := copyPack.Commit(ctx, nil); domain.AsError(err).Code != domain.CodeForbidden {
						return errors.New("copied historical pack accepted")
					}
					return f.s.config.Content.WithRollbackParticipant(ctx, scope, j.CurrentContent, f.source.ContentBefore, target, f.actor, func(items *content.RollbackParticipant) error {
						expiredContent = items
						copyItems := copiedRollbackCapability(items)
						if err := copyItems.ValidatePostimages(ctx); domain.AsError(err).Code != domain.CodeForbidden {
							return errors.New("copied historical content accepted")
						}
						if err := items.ValidatePostimages(ctx); err != nil {
							return err
						}
						if err := items.Commit(ctx, &sql.Tx{}); domain.AsError(err).Code != domain.CodeForbidden {
							return errors.New("foreign historical content transaction accepted")
						}
						if err := pack.Commit(ctx, &sql.Tx{}); domain.AsError(err).Code != domain.CodeForbidden {
							return errors.New("foreign historical pack transaction accepted")
						}
						if err := target.Commit(ctx, &sql.Tx{}); domain.AsError(err).Code != domain.CodeForbidden {
							return errors.New("foreign historical lifecycle transaction accepted")
						}
						return nil
					})
				})
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = expiredLifecycle.State(); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("expired target accepted", err)
	}
	if err = expiredContent.ValidatePostimages(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("expired content accepted", err)
	}
	if err = expiredPack.Commit(t.Context(), nil); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("expired pack accepted", err)
	}
}

// Deliberately clone a quiescent authority object to prove that its embedded
// original-owner identity rejects copies. Ordinary Go lock copying is forbidden.
func copiedRollbackCapability[T any](original *T) *T {
	copy := new(T)
	reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(original).Elem())
	return copy
}

func TestExplicitRollbackKeepsCurrentMemoryAndHistoricalRowsImmutable(t *testing.T) {
	f := newRollbackFixture(t)
	oldRaw, err := json.Marshal(map[string]any{"generation": f.source.Before.Generation, "plan": f.source.Before.Plan, "spec": f.source.Before.Spec})
	if err != nil {
		t.Fatal(err)
	}
	// A later approved manager memory setting is retained through world rewind.
	f.source.After.Spec.Args = []string{"-Xms512M", "-Xmx4096M", "-jar", "new-fixture.jar", "nogui"}
	newRaw, _ := json.Marshal(map[string]any{"generation": f.source.After.Generation, "plan": f.source.After.Plan, "spec": f.source.After.Spec})
	if _, err = f.database.ExecContext(t.Context(), "UPDATE instances SET memory_mib=4096,updated_at=? WHERE id='alpha'", stamp()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.database.ExecContext(t.Context(), "UPDATE lifecycle_installations SET installation_json=? WHERE instance_id='alpha'", string(newRaw)); err != nil {
		t.Fatal(err)
	}
	sourceRaw, _ := json.Marshal(f.source)
	if _, err = f.database.ExecContext(t.Context(), "UPDATE update_transactions SET journal_json=? WHERE id=?", string(sourceRaw), f.parent); err != nil {
		t.Fatal(err)
	}
	if done := f.enqueue(t); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	state, err := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Spec.Args) < 2 || state.Spec.Args[1] != "-Xmx4096M" {
		t.Fatalf("current manager memory lost: %+v", state.Spec)
	}
	var retained string
	if err = f.database.QueryRowContext(t.Context(), "SELECT installation_json FROM lifecycle_generations WHERE instance_id='alpha' AND generation=?", f.source.Before.Generation).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != string(oldRaw) {
		t.Fatal("rollback overwrote retained historical generation")
	}
}

func TestExplicitRollbackRestoresHistoricalAbsenceOfPackBaseline(t *testing.T) {
	f := newRollbackFixture(t)
	f.source.PackBefore = nil
	f.source.Plan.Snapshot.PackRevision = "none"
	raw, _ := json.Marshal(f.source)
	if _, err := f.database.ExecContext(t.Context(), "UPDATE update_transactions SET journal_json=? WHERE id=?", string(raw), f.parent); err != nil {
		t.Fatal(err)
	}
	if done := f.enqueue(t); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	pack, err := f.s.config.Packs.Snapshot(t.Context(), "alpha")
	if err != nil || pack.Baseline != nil {
		t.Fatalf("historical nil baseline not restored: %+v %v", pack, err)
	}
}

func TestExplicitRollbackCapturesCurrentTupleBeforeInheritedActivationBlockerIsCleared(t *testing.T) {
	f := newRollbackFixture(t)
	data := pathsDataDir(f.s.config.Paths, "alpha")
	if err := launcher.MarkActivationPending(t.Context(), data, "alpha"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.seal = func(ctx context.Context, _ string, _ domain.LaunchSpec) error {
		calls++
		if calls > 2 {
			return nil
		}
		var version, manifestRaw string
		if err := f.database.QueryRowContext(ctx, "SELECT minecraft_version FROM instances WHERE id='alpha'").Scan(&version); err != nil {
			return err
		}
		if version != "1.18.2" {
			return errors.New("old selection installed before restored world pointer commit")
		}
		if err := f.database.QueryRowContext(ctx, "SELECT manifest_json FROM backups WHERE id!=? AND state='ready'", f.backup.ID).Scan(&manifestRaw); err != nil {
			return err
		}
		var manifest backup.Manifest
		if json.Unmarshal([]byte(manifestRaw), &manifest) != nil || manifest.Version != "1.18.2" || manifest.Runtime != "r17" {
			return errors.New("safety capture did not retain CURRENT tuple/runtime")
		}
		if err := launcher.CheckActivationReady(ctx, data, "alpha"); err == nil {
			return errors.New("inherited activation or world guard removed before rollback finish")
		}
		return nil
	}
	if done := f.enqueue(t); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	if err := launcher.CheckActivationReady(t.Context(), data, "alpha"); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitRollbackHistoricalRuntimeFailureNeverRewindsWorld(t *testing.T) {
	f := newRollbackFixture(t)
	f.runtime.failID = "r8"
	if done := f.enqueue(t); done.State != domain.JobFailed {
		t.Fatal(done)
	}
	data, err := os.ReadFile(filepath.Join(f.dirs.Server, "world/level.dat"))
	if err != nil || string(data) != "current migrated world" {
		t.Fatalf("unverified historical runtime rewound world=%q %v", data, err)
	}
	id, raw := f.pending(t)
	f.runtime.failID = ""
	f.restartPaused(t)
	if err := f.c.Reconcile(t.Context(), "alpha", id, raw); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitRollbackFreshAdministratorRevocationAfterWorldMovesPreventsPointerCommit(t *testing.T) {
	f := newRollbackFixture(t)
	calls := 0
	f.seal = func(ctx context.Context, _ string, _ domain.LaunchSpec) error {
		calls++
		if calls == 2 {
			_, err := f.database.ExecContext(ctx, "UPDATE users SET role='operator' WHERE id=1")
			return err
		}
		return nil
	}
	if done := f.enqueue(t); done.State != domain.JobFailed {
		t.Fatal("revoked administrator committed rollback", done)
	}
	var generation, version string
	if err := f.database.QueryRowContext(t.Context(), "SELECT i.minecraft_version,l.generation FROM instances i JOIN lifecycle_installations l ON l.instance_id=i.id WHERE i.id='alpha'").Scan(&version, &generation); err != nil {
		t.Fatal(err)
	}
	if version != "1.18.2" || generation != f.source.After.Generation {
		t.Fatalf("revoked authority changed pointers version=%s generation=%s", version, generation)
	}
	if err := launcher.CheckActivationReady(t.Context(), pathsDataDir(f.s.config.Paths, "alpha"), "alpha"); err == nil {
		t.Fatal("role revocation unblocked partially restored world")
	}
}

func TestRollbackHistoricalProofDrainsBeforeCapabilityExpires(t *testing.T) {
	f := newRollbackFixture(t)
	f.seal = func(context.Context, string, domain.LaunchSpec) error {
		return errors.New("fixture before restoration")
	}
	if done := f.enqueue(t); done.State != domain.JobFailed {
		t.Fatal(done)
	}
	id, raw := f.pending(t)
	var j rollbackJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	f.seal = nil
	f.restartPaused(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	callbackReturned := make(chan struct{})
	finished := make(chan error, 1)
	proofResult := make(chan error, 1)
	go func() {
		finished <- f.s.config.Instances.WithStoppedRecovery(t.Context(), "alpha", func(session *instance.StoppedSession) error {
			return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *instance.TransactionScope) error {
				return f.s.config.Lifecycle.WithRollbackParticipant(ctx, scope, id, j.Preview.Snapshot.Generation, f.source.Before, f.actor, func(target *lifecycle.RollbackParticipant) error {
					if err := target.Seal(ctx); err != nil {
						return err
					}
					go func() {
						proofResult <- target.WithTargetProof(ctx, func(_ context.Context, proof lifecycle.TargetProof) error {
							if proof.Selection != f.source.Before.Plan.Selection {
								return errors.New("proof used current failed loader")
							}
							close(entered)
							<-release
							return nil
						})
					}()
					<-entered
					close(callbackReturned)
					return nil
				})
			})
		})
	}()
	<-callbackReturned
	select {
	case err := <-finished:
		t.Fatalf("historical participant escaped before admitted proof drained: %v", err)
	default:
	}
	if err := f.q.Resume(); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("paused recovery resumed before participant drain", err)
	}
	close(release)
	if err := <-proofResult; err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
func TestRollbackHistoryRefusesMalformedOrUnbootedSource(t *testing.T) {
	for _, raw := range []string{`{}`, `{"version":1,"boot_attempted":false}`, `{"version":2,"boot_attempted":true}`, `null`} {
		if _, err := decodeRollbackSource(json.RawMessage(raw)); err == nil {
			t.Fatalf("unsafe history admitted: %s", raw)
		}
	}
}
