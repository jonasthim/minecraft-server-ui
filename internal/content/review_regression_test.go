package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func (f *fixture) installCatalogItem(t *testing.T, project, descriptor string, deps []catalog.Dependency) Installed {
	t.Helper()
	data := jar(t, descriptor)
	artifact := filepath.Join(t.TempDir(), "verified.artifact")
	if e := os.WriteFile(artifact, data, 0600); e != nil {
		t.Fatal(e)
	}
	f.catalog.path = artifact
	selection := catalog.Selection{Provider: catalog.Modrinth, ProjectID: project, VersionID: project + "-v1", FileID: project + "-file"}
	f.catalog.version = catalog.Version{ID: selection.VersionID, ProjectID: project, Compatibility: catalog.Compatibility{Minecraft: []string{"1.21.1"}, Loaders: []string{"fabric"}, ServerSide: "supported", Environment: "server"}, Dependencies: deps, Files: []catalog.File{{ID: selection.FileID, Filename: project + ".jar", Size: int64(len(data)), URL: "https://cdn.modrinth.com/" + project, Hashes: map[string]string{"sha256": digest(data)}}}}
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, Selection: &selection}, operator)
	if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	items, e := f.s.List(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		if item.Selection != nil && item.Selection.ProjectID == project {
			return item
		}
	}
	t.Fatal("installed catalog item absent")
	return Installed{}
}
func mustApplyReview(t *testing.T, f *fixture, action string, item Installed) {
	t.Helper()
	p := f.preview(t, Mutation{Action: action, ContentID: item.ID}, operator)
	if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
		t.Fatal(action, j)
	}
}
func TestReviewEnableRevalidatesCompleteEnabledManifest(t *testing.T) {
	t.Run("publisher required dependency removed while disabled", func(t *testing.T) {
		f := setup(t, false)
		a := f.installCatalogItem(t, "a", fabric("a", "1.0.0"), nil)
		b := f.installCatalogItem(t, "b", fabric("b", "1.0.0"), []catalog.Dependency{{Provider: catalog.Modrinth, ProjectID: "a", VersionID: "a-v1", Relation: "required", Required: true}})
		mustApplyReview(t, f, "disable", b)
		mustApplyReview(t, f, "uninstall", a)
		p := f.preview(t, Mutation{Action: "enable", ContentID: b.ID}, operator)
		if len(p.Blockers) == 0 {
			t.Fatal("enable accepted missing publisher-only required dependency")
		}
	})
	for _, kind := range []string{"publisher outgoing", "publisher reverse", "descriptor reverse", "duplicate descriptor", "duplicate publisher", "unknown trial reverse"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t, false)
			var b Installed
			if kind == "unknown trial reverse" {
				u := f.upload(t, "b.jar", jar(t, fabric("b", "1.0.0")))
				p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
				if j := f.apply(t, p, admin); j.State != domain.JobSucceeded {
					t.Fatal(j)
				}
				items, _ := f.s.List(ctx, "alpha")
				b = items[0]
			} else {
				deps := []catalog.Dependency{}
				if kind == "publisher outgoing" {
					deps = append(deps, catalog.Dependency{Provider: catalog.Modrinth, ProjectID: "c", Relation: "incompatible"})
				}
				b = f.installCatalogItem(t, "b", fabric("b", "1.0.0"), deps)
			}
			mustApplyReview(t, f, "disable", b)
			descriptor := fabric("c", "1.0.0")
			deps := []catalog.Dependency{}
			if kind == "descriptor reverse" || kind == "unknown trial reverse" {
				descriptor = `{"id":"c","version":"1.0.0","environment":"server","depends":{"minecraft":"1.21.1","fabricloader":">=0.16.0"},"breaks":{"b":"*"}}`
			}
			if kind == "duplicate descriptor" {
				descriptor = fabric("b", "2.0.0")
			}
			if kind == "publisher reverse" {
				deps = append(deps, catalog.Dependency{Provider: catalog.Modrinth, ProjectID: "b", Relation: "incompatible"})
			}
			c := f.installCatalogItem(t, "c", descriptor, deps)
			if kind == "duplicate publisher" {
				c.Selection.ProjectID = b.Selection.ProjectID
				raw, _ := json.Marshal(c)
				if _, e := f.db.Exec("UPDATE content_items SET manifest_json=? WHERE id=?", string(raw), c.ID); e != nil {
					t.Fatal(e)
				}
			}
			p := f.preview(t, Mutation{Action: "enable", ContentID: b.ID}, operator)
			if len(p.Blockers) == 0 {
				t.Fatal("enable accepted known contradiction", kind)
			}
		})
	}
}
func TestReviewFailedAdmissionPreviewCanRetryAndDiscard(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	f.g.mu.Lock()
	f.g.running = true
	f.g.mu.Unlock()
	first := f.apply(t, p, operator)
	if first.State != domain.JobFailed {
		t.Fatal(first)
	}
	f.g.mu.Lock()
	f.g.running = false
	f.g.mu.Unlock()
	second, e := f.s.Enqueue(ctx, "alpha", p.ID, operator)
	if e != nil {
		t.Fatal("terminal admission failure stranded preview", e)
	}
	if second.ID == first.ID {
		t.Fatal("retry reused terminal durable job")
	}
	if done := f.wait(t, second.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	p = f.preview(t, Mutation{Action: "enable", ContentID: item.ID}, operator)
	f.g.mu.Lock()
	f.g.running = true
	f.g.mu.Unlock()
	if j := f.apply(t, p, operator); j.State != domain.JobFailed {
		t.Fatal(j)
	}
	if e = f.s.Discard(ctx, "alpha", p.ID, operator); e != nil {
		t.Fatal(e)
	}
	var count int
	f.db.QueryRow("SELECT count(*) FROM content_previews WHERE id=?", p.ID).Scan(&count)
	if count != 0 {
		t.Fatal("discard reported success but retained failed preview")
	}
}
func TestReviewQueuedCancellationAndLiveOwnership(t *testing.T) {
	f := setup(t, true)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	j, e := f.s.Enqueue(ctx, "alpha", p.ID, admin)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Enqueue(ctx, "alpha", p.ID, admin); e == nil {
		t.Fatal("duplicate live queued job admitted")
	}
	if e = f.s.Discard(ctx, "alpha", p.ID, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("live queued preview discard did not refuse", e)
	}
	if e = f.q.Cancel(ctx, j.ID, admin); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Discard(ctx, "alpha", p.ID, admin); e != nil {
		t.Fatal("cancelled queued preview not discardable", e)
	}
	var n int
	f.db.QueryRow("SELECT count(*) FROM content_previews WHERE id=?", p.ID).Scan(&n)
	if n != 0 {
		t.Fatal("cancelled preview retained")
	}
}

type reviewBlockingInstances struct {
	Instances
	entered, release chan struct{}
}

func (b *reviewBlockingInstances) WithStoppedJob(ctx context.Context, e *jobs.Execution, fn func(*instance.StoppedSession) error) error {
	return b.Instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error { close(b.entered); <-b.release; return fn(session) })
}
func TestReviewRunningJobCannotLosePreviewOrStages(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	blocked := &reviewBlockingInstances{Instances: f.s.instances, entered: make(chan struct{}), release: make(chan struct{})}
	f.s.instances = blocked
	j, e := f.s.Enqueue(ctx, "alpha", p.ID, operator)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("worker not admitted")
	}
	defer func() {
		select {
		case <-blocked.release:
		default:
			close(blocked.release)
		}
	}()
	if _, e = f.s.Enqueue(ctx, "alpha", p.ID, operator); e == nil {
		t.Fatal("running preview enqueued twice")
	}
	if e = f.s.Discard(ctx, "alpha", p.ID, operator); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("running preview discard did not refuse", e)
	}
	close(blocked.release)
	if done := f.wait(t, j.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
}
func TestReviewCollisionScanRefusesOverBound(t *testing.T) {
	f := setup(t, false)
	mods := filepath.Join(f.dirs.Server, "mods")
	if e := os.Mkdir(mods, 0700); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 4097; i++ {
		if e := os.WriteFile(filepath.Join(mods, fmt.Sprintf("entry-%04d.txt", i)), nil, 0600); e != nil {
			t.Fatal(e)
		}
	}
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = f.s.checkCollision(ctx, root, "mods/fresh.jar", nil); domain.AsError(e).Code != domain.CodeConflict || !strings.Contains(domain.AsError(e).Message, "limit") {
		t.Fatal("unbounded directory accepted", e)
	}
	if e = os.Remove(filepath.Join(mods, "entry-4096.txt")); e != nil {
		t.Fatal(e)
	}
	if e = f.s.checkCollision(ctx, root, "mods/fresh.jar", nil); e != nil {
		t.Fatal("exact collision bound refused", e)
	}
}

func TestReviewAdmissionFailuresReuseOnePreviewWithoutExhaustingQuota(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	f.g.mu.Lock()
	f.g.running = true
	f.g.mu.Unlock()
	for i := 0; i < 34; i++ {
		job, e := f.s.Enqueue(ctx, "alpha", p.ID, operator)
		if e != nil {
			t.Fatal("retry stranded after admission failure", i, e)
		}
		if terminal := f.wait(t, job.ID); terminal.State != domain.JobFailed {
			t.Fatal(terminal)
		}
	}
	var previews int
	f.db.QueryRow("SELECT count(*) FROM content_previews WHERE instance_id='alpha'").Scan(&previews)
	if previews != 1 {
		t.Fatal("retry consumed preview quota", previews)
	}
	if e := f.s.Discard(ctx, "alpha", p.ID, operator); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Preview(ctx, "alpha", Mutation{Action: "disable", ContentID: item.ID}, operator); e != nil {
		t.Fatal("discard did not release quota", e)
	}
}
func TestReviewQueuedCancellationAllowsSafeRetry(t *testing.T) {
	f := setup(t, true)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	first, e := f.s.Enqueue(ctx, "alpha", p.ID, admin)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.q.Cancel(ctx, first.ID, admin); e != nil {
		t.Fatal(e)
	}
	second, e := f.s.Enqueue(ctx, "alpha", p.ID, admin)
	if e != nil || second.ID == first.ID {
		t.Fatal("cancelled queued job stranded preview", second, e)
	}
	if e = f.q.Resume(); e != nil {
		t.Fatal(e)
	}
	if done := f.wait(t, second.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
}
func TestReviewLiveJobAndStagesSurvivePreviewExpiry(t *testing.T) {
	f := setup(t, true)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	if _, e := f.s.Enqueue(ctx, "alpha", p.ID, admin); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec("UPDATE content_previews SET created_at='2000-01-01T00:00:00Z' WHERE id=?", p.ID); e != nil {
		t.Fatal(e)
	}
	f.db.Exec("UPDATE content_stages SET created_at='2000-01-01T00:00:00Z'")
	f.s.mu.Lock()
	e := f.s.expire(ctx, "alpha")
	f.s.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	var n int
	f.db.QueryRow("SELECT count(*) FROM content_previews WHERE id=?", p.ID).Scan(&n)
	if n != 1 {
		t.Fatal("expiry deleted a live job's preview")
	}
	if _, file, e := f.s.openStage(ctx, "alpha", u.ID); e != nil {
		t.Fatal("expiry removed a live job's stage", e)
	} else {
		file.Close()
	}
}
func TestReviewLegacyUnboundQueuedMarkerWithoutJobCanRetry(t *testing.T) {
	f := setup(t, true)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	if _, e := f.db.Exec("UPDATE content_previews SET state='queued' WHERE id=?", p.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Enqueue(ctx, "alpha", p.ID, admin); e != nil {
		t.Fatal("legacy crash before durable enqueue stranded preview", e)
	}
}
func TestReviewDurableEnqueueCrashRepairsBindingOnRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "manager.db")
	f := setupDatabase(t, true, dbPath)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	f.s.fault = func(phase string) error {
		if phase == "enqueue" {
			return fmt.Errorf("manager interrupted before binding")
		}
		return nil
	}
	job, e := f.s.Enqueue(ctx, "alpha", p.ID, admin)
	if e == nil || job.ID == "" {
		t.Fatal("durable enqueue crash not injected", job, e)
	}
	var bound sql.NullString
	var state string
	if e = f.db.QueryRow("SELECT state,job_id FROM content_previews WHERE id=?", p.ID).Scan(&state, &bound); e != nil || state != "preview" || bound.Valid {
		t.Fatal("crash did not precede preview binding", state, bound, e)
	}
	duplicatePayload, _ := json.Marshal(map[string]any{"preview_id": p.ID, "actor_id": admin.ID})
	if _, e = f.q.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: "content_mutation", Payload: duplicatePayload, RequestedBy: admin.ID, Cancellable: true}); e == nil {
		t.Fatal("durable unique live-job index admitted an unbound duplicate")
	}
	if _, e = f.s.Enqueue(ctx, "alpha", p.ID, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("unbound live job admitted duplicate", e)
	}
	if e = f.s.Discard(ctx, "alpha", p.ID, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("unbound live job lost its preview", e)
	}
	// Simulate the unbound SQL receipt across an actual SQLite close/reopen.
	if _, e = f.db.Exec("UPDATE content_previews SET state='preview',job_id=NULL WHERE id=?", p.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.q.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	database, e := db.Open(ctx, dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	queue := jobs.New(database, nil)
	defer queue.Stop(ctx)
	instances := instance.New(database, f.p, f.g, instance.WithJobMutationGuard(f.g))
	service, e := New(Config{DB: database, Paths: f.p, Instances: instances, Queue: queue, Recovery: f.g})
	if e != nil {
		t.Fatal(e)
	}
	if e = service.Register(); e != nil {
		t.Fatal(e)
	}
	if e = queue.StartPaused(ctx); e != nil {
		t.Fatal(e)
	}
	if e = service.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if e = database.QueryRow("SELECT state,job_id FROM content_previews WHERE id=?", p.ID).Scan(&state, &bound); e != nil || state != "queued" || bound.String != job.ID {
		t.Fatal("startup failed to repair durable job ownership", state, bound, e)
	}
	if _, e = service.Enqueue(ctx, "alpha", p.ID, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("startup admitted duplicate retained job", e)
	}
	if e = queue.Resume(); e != nil {
		t.Fatal(e)
	}
	if done := (&fixture{q: queue}).wait(t, job.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	var count int
	database.QueryRow("SELECT count(*) FROM jobs WHERE kind='content_mutation'").Scan(&count)
	if count != 1 {
		t.Fatal("crash recovery duplicated jobs", count)
	}
}

func TestReviewApplyRejectsLegacyUnsafeEnablePreview(t *testing.T) {
	f := setup(t, false)
	a := f.installCatalogItem(t, "a", fabric("a", "1.0.0"), nil)
	b := f.installCatalogItem(t, "b", fabric("b", "1.0.0"), []catalog.Dependency{{Provider: catalog.Modrinth, ProjectID: "a", Relation: "required", Required: true}})
	mustApplyReview(t, f, "disable", b)
	mustApplyReview(t, f, "uninstall", a)
	p := f.preview(t, Mutation{Action: "enable", ContentID: b.ID}, operator)
	if len(p.Blockers) == 0 {
		t.Fatal("preview did not identify missing dependency")
	}
	private, e := f.s.loadPreview(ctx, "alpha", p.ID, operator.ID)
	if e != nil {
		t.Fatal(e)
	}
	// Reproduce a persisted pre-fix preview that had no enable dependency blockers.
	private.Preview.Blockers = []string{}
	raw, _ := json.Marshal(private)
	if _, e = f.db.Exec("UPDATE content_previews SET private_json=? WHERE id=?", string(raw), p.ID); e != nil {
		t.Fatal(e)
	}
	job, e := f.s.Enqueue(ctx, "alpha", p.ID, operator)
	if e != nil {
		t.Fatal(e)
	}
	if terminal := f.wait(t, job.ID); terminal.State != domain.JobFailed {
		t.Fatal("apply trusted unsafe legacy enable preview", terminal)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "mods/b.jar.disabled")); e != nil {
		t.Fatal("failed compatibility check changed disabled content", e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "mods/b.jar")); !os.IsNotExist(e) {
		t.Fatal("unsafe legacy enable published executable", e)
	}
}
func TestReviewTerminalJobWithPendingJournalCannotBeDiscardedOrRetried(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	f.s.fault = func(phase string) error {
		if phase == "file" {
			if e := os.WriteFile(filepath.Join(f.dirs.Server, "mods/example.jar"), []byte("competing edit"), 0600); e != nil {
				return e
			}
			return fmt.Errorf("injected failure with retained recovery")
		}
		return nil
	}
	if terminal := f.apply(t, p, operator); terminal.State != domain.JobFailed {
		t.Fatal(terminal)
	}
	if e := f.s.Discard(ctx, "alpha", p.ID, operator); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("terminal job discarded an owned recovery preview", e)
	}
	if _, e := f.s.Enqueue(ctx, "alpha", p.ID, operator); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("terminal job retried unresolved recovery preview", e)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.State, markerName)); e != nil {
		t.Fatal("retained recovery lost boot blocker", e)
	}
}
