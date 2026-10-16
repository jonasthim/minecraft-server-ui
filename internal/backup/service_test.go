package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

type testNative struct {
	mu       sync.Mutex
	live     bool
	identity domain.ProcessIdentity
	steps    []string
	log      string
	fail     string
	expiry   time.Time
}

func (n *testNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.live {
		return domain.ProcessIdentity{}, os.ErrNotExist
	}
	return n.identity, nil
}
func (n *testNative) Inspect(_ context.Context, id domain.ProcessIdentity) (supervisor.Observation, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if id != n.identity && n.live {
		return supervisor.Observation{State: domain.StateUnknown}, errors.New("stale")
	}
	if !n.live {
		return supervisor.Observation{State: domain.StateStopped}, nil
	}
	return supervisor.Observation{State: domain.StateRunning, ProcessRunning: true}, nil
}
func (n *testNative) WithStopped(ctx context.Context, id string, fn func() error) error {
	n.mu.Lock()
	live := n.live
	n.mu.Unlock()
	if live {
		return domain.E(domain.CodeInstanceRunning, "running")
	}
	return fn()
}
func (n *testNative) ArmSaveLease(ctx context.Context, id domain.ProcessIdentity, lease string, expiry time.Time) error {
	n.mu.Lock()
	n.steps = append(n.steps, "save-off")
	n.expiry = expiry
	fail := n.fail
	n.mu.Unlock()
	if fail == "arm" {
		return errors.New("injected arm failure")
	}
	return nil
}
func (n *testNative) Command(ctx context.Context, id domain.ProcessIdentity, text string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.steps = append(n.steps, text)
	if n.fail == text {
		return errors.New("injected command failure")
	}
	response := ""
	switch text {
	case "save-all flush":
		response = "Saved the game"
	case "save-on":
		response = "Automatic saving is now enabled"
	}
	f, err := os.OpenFile(n.log, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("[12:00:00] [Server thread/INFO]: " + response + "\n")
	return err
}
func (n *testNative) ReleaseSaveLease(ctx context.Context, id domain.ProcessIdentity, lease string) error {
	return n.Command(ctx, id, "save-on")
}

type testGuard struct {
	queue  *jobs.Service
	native *testNative
}

func (g testGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { return g.native.WithStopped(ctx, id, func() error { return fn(id) }) })
}
func (g testGuard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.queue.WithPausedInstance(ctx, id, func() error { return g.native.WithStopped(ctx, id, fn) })
}

var testActor = domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}

func fixture(t *testing.T, live bool) (*Service, *testNative, string) {
	t.Helper()
	database, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	data := t.TempDir()
	paths, err := instance.NewPaths(data)
	if err != nil {
		t.Fatal(err)
	}
	native := &testNative{identity: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "first"}}
	instances := instance.New(database, paths, native)
	if _, err = database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at)VALUES(1,'admin','admin','2026-10-16','2026-10-16')`); err != nil {
		t.Fatal(err)
	}
	_, err = instances.Create(t.Context(), testActor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: domain.InstanceConfig{MOTD: "Current", Difficulty: "normal", GameMode: "survival", MaxPlayers: 20, OnlineMode: true, PVP: true, ViewDistance: 10, SimulationDistance: 10, LevelName: "world", Extra: map[string]string{"custom": "current", "delete-me": "current"}}})
	if err != nil {
		t.Fatal(err)
	}
	dirs, _ := paths.For("alpha")
	native.log = filepath.Join(dirs.Logs, "console.log")
	os.WriteFile(native.log, []byte("old\n"), 0600)
	os.MkdirAll(filepath.Join(dirs.Server, "world/region"), 0700)
	os.WriteFile(filepath.Join(dirs.Server, "world/level.dat"), []byte("original"), 0600)
	os.WriteFile(filepath.Join(dirs.Server, "world/region/r.0.0.mca"), []byte("region"), 0600)
	native.live = live
	q := jobs.New(database, nil)
	guard := testGuard{q, native}
	instances = instance.New(database, paths, native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	controls := console.New(database, paths, native, instances, q)
	s := New(Config{DB: database, Paths: paths, Instances: instances, Jobs: q, Console: controls, Native: native, Worlds: world.New(database, instances, paths, q)})
	if err = s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Stop(context.Background()) })
	return s, native, data
}
func await(t *testing.T, q *jobs.Service, id string) jobs.Job {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		j, err := q.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != domain.JobQueued && j.State != domain.JobRunning {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job timeout")
	return jobs.Job{}
}
func makeBackup(t *testing.T, s *Service, kind domain.BackupKind) domain.Backup {
	t.Helper()
	job, err := s.Enqueue(t.Context(), "alpha", kind, domain.BackupManual, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.queue.Resume(); err != nil {
		t.Fatal(err)
	}
	done := await(t, s.queue, job.ID)
	if done.State != domain.JobSucceeded {
		t.Fatalf("backup job failed: %+v", done)
	}
	records, err := s.List(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range records {
		if r.JobID == job.ID {
			return r.Backup
		}
	}
	t.Fatal("no backup record")
	return domain.Backup{}
}
func TestLiveBackupLeaseReleasePrecedesArchiveAndSecretsStayPrivate(t *testing.T) {
	s, native, _ := fixture(t, true)
	dirs, _ := s.paths.For("alpha")
	os.WriteFile(filepath.Join(dirs.Server, "server.properties"), []byte("management-server-secret=manager-private\ncustom=value\n"), 0600)
	os.MkdirAll(filepath.Join(dirs.Server, "plugins/Example"), 0700)
	os.WriteFile(filepath.Join(dirs.Server, "plugins/Example/config.yml"), []byte("password: addon-private\n"), 0600)
	os.WriteFile(filepath.Join(dirs.Server, "manager.db"), []byte("manager-private"), 0600)
	os.MkdirAll(filepath.Join(dirs.Server, ".manager"), 0700)
	os.WriteFile(filepath.Join(dirs.Server, ".manager/launch.json"), []byte("manager-private"), 0600)
	s.fault = func(phase string) error {
		if phase == "archive" {
			native.mu.Lock()
			defer native.mu.Unlock()
			if len(native.steps) < 3 || native.steps[len(native.steps)-1] != "save-on" {
				return errors.New("compression before saving release")
			}
		}
		return nil
	}
	record := makeBackup(t, s, domain.BackupFull)
	if record.State != domain.BackupReady || !record.Protected {
		t.Fatalf("record %+v", record)
	}
	download, err := s.Download(t.Context(), "alpha", record.ID, testActor)
	if err != nil {
		t.Fatal(err)
	}
	defer download.Close()
	manifest, err := validateArchive(t.Context(), download.File, record.SizeBytes, defaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range manifest.Files {
		if strings.Contains(f.Path, "manager.db") || strings.Contains(f.Path, ".manager") {
			t.Fatal("manager state included")
		}
		if f.Path == "plugins/Example/config.yml" {
			found = true
		}
	}
	if !found {
		t.Fatal("local addon data missing")
	}
	if native.expiry.Sub(record.CreatedAt) > MaxSaveLease+time.Second {
		t.Fatal("extended deadline")
	}
}
func TestBackupFailuresNeverPublishAndAlwaysRelease(t *testing.T) {
	for _, phase := range []string{"capture", "after_release", "archive", "validation", "publish", "commit"} {
		t.Run(phase, func(t *testing.T) {
			s, native, _ := fixture(t, true)
			s.fault = func(p string) error {
				if p == phase {
					return errors.New("injected")
				}
				return nil
			}
			job, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, domain.BackupManual, testActor)
			if err != nil {
				t.Fatal(err)
			}
			s.queue.Resume()
			done := await(t, s.queue, job.ID)
			if done.State != domain.JobFailed {
				t.Fatalf("failure hidden: %+v", done)
			}
			native.mu.Lock()
			defer native.mu.Unlock()
			if len(native.steps) < 3 || native.steps[len(native.steps)-1] != "save-on" {
				t.Fatalf("saving not released %v", native.steps)
			}
			records, err := s.List(t.Context(), "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || records[0].State == domain.BackupReady {
				t.Fatal("failed backup usable")
			}
		})
	}
}
func TestRestoreSafetyFailureLeavesWorldAndInterruptedRestoreResumes(t *testing.T) {
	s, _, data := fixture(t, false)
	record := makeBackup(t, s, domain.BackupFull)
	dirs, _ := s.paths.For("alpha")
	os.WriteFile(filepath.Join(dirs.Server, "world/level.dat"), []byte("current-world"), 0600)
	s.fault = func(p string) error {
		if p == "safety" {
			return errors.New("safety failure")
		}
		return nil
	}
	job, err := s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if await(t, s.queue, job.ID).State != domain.JobFailed {
		t.Fatal("safety failure hidden")
	}
	raw, _ := os.ReadFile(filepath.Join(dirs.Server, "world/level.dat"))
	if string(raw) != "current-world" {
		t.Fatal("world changed before safety backup")
	}
	s.fault = func(p string) error {
		if p == "restore_old_moved" {
			return errors.New("interrupted move")
		}
		return nil
	}
	job, err = s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
	if err != nil {
		t.Fatal(err)
	}
	done := await(t, s.queue, job.ID)
	if done.State != domain.JobFailed {
		t.Fatalf("fault hidden %+v", done)
	}
	if err = launcher.CheckActivationReady(t.Context(), data, "alpha"); err == nil {
		t.Fatal("partial restore can launch")
	}
	s.queue.Stop(context.Background())
	q := jobs.New(s.db, nil)
	native := s.native.(*testNative)
	guard := testGuard{q, native}
	instances := instance.New(s.db, s.paths, native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	recovery := New(Config{DB: s.db, Paths: s.paths, Instances: instances, Jobs: q, Native: native, Worlds: world.New(s.db, instances, s.paths, q)})
	recovery.Register()
	q.StartPaused(t.Context())
	defer q.Stop(context.Background())
	if err = recovery.ReconcileRestores(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dirs.Server, "world/level.dat"))
	if string(raw) != "original" {
		t.Fatalf("confirmed restore not completed: %q", raw)
	}
	if err = launcher.CheckActivationReady(t.Context(), data, "alpha"); err != nil {
		t.Fatal(err)
	}
}

type repairLeaseNative struct{ *testNative }

func (n repairLeaseNative) ReleaseSaveLease(context.Context, domain.ProcessIdentity, string) error {
	return errors.New("old lease absent")
}
func TestLeaseRecoveryWaitsForOriginalExpiryWhenClearanceUncertain(t *testing.T) {
	s, native, _ := fixture(t, true)
	wrapper := repairLeaseNative{native}
	s.native = wrapper
	s.console = console.New(s.db, s.paths, wrapper, s.instances, s.queue)
	identity, _ := json.Marshal(native.identity)
	leaseID := strings.Repeat("a", 32)
	if _, err := s.db.Exec(`INSERT INTO backup_leases(id,instance_id,job_id,identity_json,expires_at,state,updated_at)VALUES(?,'alpha','original-job',?,?,'armed',?)`, leaseID, string(identity), time.Now().Add(time.Minute).Format(time.RFC3339Nano), stamp()); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileLeases(t.Context()); err == nil {
		t.Fatal("uncleared lease marked recovered before expiry")
	}
	var state string
	s.db.QueryRow(`SELECT state FROM backup_leases WHERE id=?`, leaseID).Scan(&state)
	if state != "unresolved" {
		t.Fatal(state)
	}
	s.db.Exec(`UPDATE backup_leases SET expires_at=? WHERE id=?`, time.Now().Add(-time.Second).Format(time.RFC3339Nano), leaseID)
	if err := s.ReconcileLeases(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT state FROM backup_leases WHERE id=?`, leaseID).Scan(&state)
	if state != "released" {
		t.Fatal(state)
	}
}

func TestRestoreRejectsUnconfirmedRunningAndVersionMismatch(t *testing.T) {
	s, native, _ := fixture(t, false)
	record := makeBackup(t, s, domain.BackupWorld)
	if _, err := s.EnqueueRestore(t.Context(), "alpha", record.ID, false, testActor); err == nil {
		t.Fatal("unconfirmed restore queued")
	}
	native.mu.Lock()
	native.live = true
	native.mu.Unlock()
	job, err := s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if await(t, s.queue, job.ID).State != domain.JobFailed {
		t.Fatal("running server restored")
	}
	native.mu.Lock()
	native.live = false
	native.mu.Unlock()
	s.db.Exec(`UPDATE instances SET minecraft_version='1.16.4' WHERE id='alpha'`)
	job, err = s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if await(t, s.queue, job.ID).State != domain.JobFailed {
		t.Fatal("mismatched version restored")
	}
	var safety int
	s.db.QueryRow(`SELECT count(*) FROM backups WHERE metadata_json LIKE '%"safety"%'`).Scan(&safety)
	if safety != 0 {
		t.Fatal("provenance mismatch passed safety gate")
	}
}

func TestRestoreNewMoveAndMaterializationInterruptionsResume(t *testing.T) {
	for _, phase := range []string{"restore_new_moved", "restore_materialize"} {
		t.Run(phase, func(t *testing.T) {
			s, _, data := fixture(t, false)
			record := makeBackup(t, s, domain.BackupFull)
			dirs, _ := s.paths.For("alpha")
			os.WriteFile(filepath.Join(dirs.Server, "world/level.dat"), []byte("current"), 0600)
			s.fault = func(p string) error {
				if p == phase {
					return errors.New("interrupted")
				}
				return nil
			}
			job, err := s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
			if err != nil {
				t.Fatal(err)
			}
			if await(t, s.queue, job.ID).State != domain.JobFailed {
				t.Fatal("restore fault hidden")
			}
			if err = launcher.CheckActivationReady(t.Context(), data, "alpha"); err == nil {
				t.Fatal("interrupted restore allowed launch")
			}
			s.queue.Stop(context.Background())
			q := jobs.New(s.db, nil)
			native := s.native.(*testNative)
			guard := testGuard{q, native}
			instances := instance.New(s.db, s.paths, native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
			recovery := New(Config{DB: s.db, Paths: s.paths, Instances: instances, Jobs: q, Native: native, Worlds: world.New(s.db, instances, s.paths, q)})
			recovery.Register()
			q.StartPaused(t.Context())
			defer q.Stop(context.Background())
			if err = recovery.ReconcileRestores(t.Context()); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(filepath.Join(dirs.Server, "world/level.dat"))
			if string(raw) != "original" {
				t.Fatal("confirmed restore not resumed")
			}
			if err = launcher.CheckActivationReady(t.Context(), data, "alpha"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRetentionPreservesQueuedRestoreAndActiveDownload(t *testing.T) {
	s, _, _ := fixture(t, false)
	record := makeBackup(t, s, domain.BackupWorld)
	if err := s.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	record.Trigger = domain.BackupAutomatic
	record.Protected = false
	record.CreatedAt = time.Now().Add(-40 * 24 * time.Hour)
	raw, _ := json.Marshal(record)
	s.db.Exec(`UPDATE backups SET metadata_json=?,created_at=? WHERE id=?`, string(raw), record.CreatedAt.Format(time.RFC3339Nano), record.ID)
	download, err := s.Download(t.Context(), "alpha", record.ID, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.applyRetention(t.Context(), "alpha", DefaultRetention, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.record(t.Context(), "alpha", record.ID); err != nil {
		t.Fatal("active export pruned")
	}
	download.Close()
	s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable)VALUES('restore-reference','alpha','backup_restore','queued',?, ?,1,1)`, stamp(), `{"backup_id":"`+record.ID+`","confirmed":true}`)
	if err = s.applyRetention(t.Context(), "alpha", DefaultRetention, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.record(t.Context(), "alpha", record.ID); err != nil {
		t.Fatal("queued restore target pruned")
	}
	s.db.Exec(`UPDATE jobs SET state='failed' WHERE id='restore-reference'`)
	if err = s.applyRetention(t.Context(), "alpha", DefaultRetention, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.record(t.Context(), "alpha", record.ID); err == nil {
		t.Fatal("unreferenced expired automatic archive retained")
	}
}

func TestRestoreRejectsUnresolvedConfigAndLifecycleRecoveryBeforeSafety(t *testing.T) {
	for _, pending := range []string{"config", "activation_row", "activation_marker"} {
		t.Run(pending, func(t *testing.T) {
			s, _, data := fixture(t, false)
			record := makeBackup(t, s, domain.BackupFull)
			dirs, _ := s.paths.For("alpha")
			os.WriteFile(filepath.Join(dirs.Server, "world/level.dat"), []byte("current"), 0600)
			switch pending {
			case "config":
				if err := os.WriteFile(filepath.Join(dirs.State, "config.pending.json"), []byte("pending config recovery"), 0600); err != nil {
					t.Fatal(err)
				}
			case "activation_row":
				if _, err := s.db.Exec(`INSERT INTO lifecycle_activations(instance_id,generation,installation_json)VALUES('alpha','pending','{}')`); err != nil {
					t.Fatal(err)
				}
			case "activation_marker":
				if err := launcher.MarkActivationPending(t.Context(), data, "alpha"); err != nil {
					t.Fatal(err)
				}
			}
			job, err := s.EnqueueRestore(t.Context(), "alpha", record.ID, true, testActor)
			if err != nil {
				t.Fatal(err)
			}
			if await(t, s.queue, job.ID).State != domain.JobFailed {
				t.Fatal("restore crossed independent recovery")
			}
			raw, _ := os.ReadFile(filepath.Join(dirs.Server, "world/level.dat"))
			if string(raw) != "current" {
				t.Fatal("world changed despite pending config recovery")
			}
			var count int
			if err = s.db.QueryRow(`SELECT count(*) FROM backups`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("safety capture began before recovery gate: count=%d %v", count, err)
			}
			if pending == "config" {
				if _, err = os.Stat(filepath.Join(dirs.State, "config.pending.json")); err != nil {
					t.Fatal("old config journal consumed by restore")
				}
			}
		})
	}
}
