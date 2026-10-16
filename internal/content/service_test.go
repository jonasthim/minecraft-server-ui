package content

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()
var admin = domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
var operator = domain.User{ID: 2, Username: "operator", Role: domain.RoleOperator}

type guard struct {
	mu      sync.Mutex
	running bool
	calls   int
}

func (g *guard) WithStopped(_ context.Context, _ string, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.running {
		return domain.E(domain.CodeInstanceRunning, "running")
	}
	return fn()
}
func (g *guard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { return g.WithStopped(ctx, id, func() error { return fn(id) }) })
}
func (g *guard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.WithStopped(ctx, id, fn)
}

type fakeCatalog struct {
	version   catalog.Version
	project   catalog.Project
	path      string
	downloads int
}

func (f *fakeCatalog) Project(context.Context, catalog.Provider, string) (catalog.Project, error) {
	return f.project, nil
}
func (f *fakeCatalog) Version(context.Context, catalog.Provider, string, string) (catalog.Version, error) {
	return f.version, nil
}
func (f *fakeCatalog) Download(_ context.Context, s catalog.Selection) (catalog.VerifiedArtifact, error) {
	f.downloads++
	b, e := os.ReadFile(f.path)
	if e != nil {
		return catalog.VerifiedArtifact{}, e
	}
	return catalog.VerifiedArtifact{Selection: s, File: f.version.Files[0], SHA256: digest(b), Path: f.path}, nil
}
func jar(t *testing.T, descriptor string) []byte {
	t.Helper()
	return jarEntries(t, map[string]string{"fabric.mod.json": descriptor, "example.class": "inert fixture"})
}
func jarEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for n, text := range entries {
		f, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write([]byte(text)); e != nil {
			t.Fatal(e)
		}
	}
	if e := z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func fabric(id, version string) string {
	return `{"id":"` + id + `","version":"` + version + `","environment":"server","depends":{"minecraft":"1.21.1","fabricloader":">=0.16.0"}}`
}

type fixture struct {
	s       *Service
	q       *jobs.Service
	db      *sql.DB
	p       instance.Paths
	g       *guard
	catalog *fakeCatalog
	dirs    instance.Directories
}

func setup(t *testing.T, paused bool) *fixture { return setupDatabase(t, paused, "") }
func setupDatabase(t *testing.T, paused bool, diskPath string) *fixture {
	t.Helper()
	var database *sql.DB
	var e error
	if diskPath == "" {
		database, e = db.OpenMemory(ctx)
	} else {
		database, e = db.Open(ctx, diskPath)
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	for _, u := range []domain.User{admin, operator, {ID: 3, Username: "viewer", Role: domain.RoleViewer}} {
		if _, e = database.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(?,?,?,'now','now')", u.ID, u.Username, u.Role); e != nil {
			t.Fatal(e)
		}
	}
	paths, e := instance.NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := &guard{}
	instances := instance.New(database, paths, g, instance.WithJobMutationGuard(g))
	_, e = instances.Create(ctx, admin, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.16.5", JavaMajor: 21, MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if e != nil {
		t.Fatal(e)
	}
	// Package fixture represents lifecycle's reviewed installed marker; it does
	// not qualify real provider installation, EULA acceptance or game execution.
	if _, e = database.Exec("UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'"); e != nil {
		t.Fatal(e)
	}
	q := jobs.New(database, nil)
	cat := &fakeCatalog{project: catalog.Project{Kind: catalog.Mod}}
	s, e := New(Config{DB: database, Paths: paths, Instances: instances, Queue: q, Catalog: cat, Recovery: g})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Register(); e != nil {
		t.Fatal(e)
	}
	if paused {
		e = q.StartPaused(ctx)
	} else {
		e = q.Start(ctx)
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c, cancel := context.WithTimeout(ctx, time.Second); defer cancel(); q.Stop(c) })
	dirs, _ := paths.For("alpha")
	return &fixture{s, q, database, paths, g, cat, dirs}
}
func (f *fixture) upload(t *testing.T, name string, b []byte) Upload {
	t.Helper()
	u, e := f.s.StageUpload(ctx, "alpha", name, bytes.NewReader(b), admin)
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func (f *fixture) preview(t *testing.T, m Mutation, u domain.User) Preview {
	t.Helper()
	p, e := f.s.Preview(ctx, "alpha", m, u)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *fixture) apply(t *testing.T, p Preview, u domain.User) jobs.Job {
	t.Helper()
	if len(p.Blockers) > 0 {
		t.Fatalf("blockers: %v", p.Blockers)
	}
	j, e := f.s.Enqueue(ctx, "alpha", p.ID, u)
	if e != nil {
		t.Fatal(e)
	}
	return f.wait(t, j.ID)
}
func (f *fixture) wait(t *testing.T, id string) jobs.Job {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		j, e := f.q.Get(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == domain.JobSucceeded || j.State == domain.JobFailed || j.State == domain.JobCancelled {
			return j
		}
		select {
		case <-deadline:
			t.Fatal("job timeout")
		case <-time.After(time.Millisecond):
		}
	}
}
func (f *fixture) installUnknown(t *testing.T) Installed {
	u := f.upload(t, "example.jar", jar(t, fabric("example", "1.0.0")))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	if j := f.apply(t, p, admin); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	m, e := f.s.Snapshot(ctx, "alpha")
	if e != nil || len(m.Items) != 1 {
		t.Fatal(m, e)
	}
	return m.Items[0]
}
func TestUnknownTrialLifecyclePreservesConfigurationAndPins(t *testing.T) {
	f := setup(t, false)
	if e := os.MkdirAll(filepath.Join(f.dirs.Server, "config"), 0700); e != nil {
		t.Fatal(e)
	}
	config := []byte("# keep comments\npassword=private-canary\n")
	configPath := filepath.Join(f.dirs.Server, "config/example.toml")
	os.WriteFile(configPath, config, 0600)
	i := f.installUnknown(t)
	if !i.Unknown || !i.Pinned || i.TrialTuple == nil {
		t.Fatal(i)
	}
	m, _ := f.s.Snapshot(ctx, "alpha")
	if len(m.AutomaticBlockers) == 0 {
		t.Fatal("unknown trial lost blocker")
	}
	for _, action := range []string{"disable", "enable", "disable"} {
		p := f.preview(t, Mutation{Action: action, ContentID: i.ID}, operator)
		if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
			t.Fatal(action, j)
		}
	}
	u := f.upload(t, "example-new.jar", jar(t, fabric("example", "2.0.0")))
	p := f.preview(t, Mutation{Action: "update", ContentID: i.ID, Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	if j := f.apply(t, p, admin); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	m, _ = f.s.Snapshot(ctx, "alpha")
	if m.Items[0].Enabled || !m.Items[0].Pinned || m.Items[0].Version != "2.0.0" {
		t.Fatal(m)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods/example-new.jar.disabled")); e != nil {
		t.Fatal(e)
	}
	p = f.preview(t, Mutation{Action: "uninstall", ContentID: i.ID}, operator)
	if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	b, _ := os.ReadFile(configPath)
	if !bytes.Equal(b, config) {
		t.Fatal("configuration changed")
	}
	rows, e := f.db.Query("SELECT details_json FROM audit_log WHERE action LIKE 'content.%'")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		if strings.Contains(raw, "private-canary") {
			t.Fatal("config leaked in audit")
		}
	}
}
func TestLocalChangesAndStalePreviewArePreserved(t *testing.T) {
	f := setup(t, false)
	i := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: i.ID}, operator)
	name := filepath.Join(f.dirs.Server, "mods/example.jar")
	os.WriteFile(name, []byte("local modification"), 0600)
	j := f.apply(t, p, operator)
	if j.State != domain.JobFailed {
		t.Fatal(j)
	}
	b, _ := os.ReadFile(name)
	if string(b) != "local modification" {
		t.Fatal("local edit overwritten")
	}
	if _, e := f.s.Preview(ctx, "alpha", Mutation{Action: "uninstall", ContentID: i.ID}, operator); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
}
func TestRunningInstanceIsNeverStopped(t *testing.T) {
	f := setup(t, false)
	i := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: i.ID}, operator)
	f.g.mu.Lock()
	f.g.running = true
	before := f.g.calls
	f.g.mu.Unlock()
	j := f.apply(t, p, operator)
	if j.State != domain.JobFailed {
		t.Fatal(j)
	}
	f.g.mu.Lock()
	defer f.g.mu.Unlock()
	if !f.g.running || f.g.calls != before+1 {
		t.Fatal("native guard was bypassed or reacquired")
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods/example.jar")); e != nil {
		t.Fatal(e)
	}
}
func TestCurrentActorRevocationBlocksQueuedApply(t *testing.T) {
	f := setup(t, true)
	u := f.upload(t, "example.jar", jar(t, fabric("example", "1.0.0")))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	j, e := f.s.Enqueue(ctx, "alpha", p.ID, admin)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(4,'second-admin','admin','now','now')"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Exec("UPDATE users SET role='viewer' WHERE id=1"); e != nil {
		t.Fatal(e)
	}
	if e = f.q.Resume(); e != nil {
		t.Fatal(e)
	}
	if v := f.wait(t, j.ID); v.State != domain.JobFailed {
		t.Fatal(v)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "mods/example.jar")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestInterruptedPublicationRollsBackPreimages(t *testing.T) {
	f := setup(t, false)
	i := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: i.ID}, operator)
	f.s.fault = func(phase string) error {
		if phase == "file" {
			return errors.New("injected disk fault")
		}
		return nil
	}
	j := f.apply(t, p, operator)
	if j.State != domain.JobFailed {
		t.Fatal(j)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods/example.jar")); e != nil {
		t.Fatal("preimage not restored", e)
	}
	var journals int
	f.db.QueryRow("SELECT count(*) FROM content_journals").Scan(&journals)
	if journals != 0 {
		t.Fatal("clean rollback retained journal")
	}
	m, _ := f.s.Snapshot(ctx, "alpha")
	if !m.Items[0].Enabled {
		t.Fatal("manifest advanced on failure")
	}
}
func TestRecoveryPreservesConcurrentEditAndRetainsJournal(t *testing.T) {
	f := setup(t, false)
	i := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: i.ID}, operator)
	private, e := f.s.loadPreview(ctx, "alpha", p.ID, operator.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(private)
	f.db.Exec("INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES('alpha',?,?,'applying')", p.ID, string(raw))
	name := filepath.Join(f.dirs.Server, "mods/example.jar")
	os.WriteFile(name, []byte("competing owner edit"), 0600)
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = f.s.recoverJournal(ctx, root, "alpha"); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(name)
	if string(b) != "competing owner edit" {
		t.Fatal("recovery overwrote edit")
	}
	var state string
	f.db.QueryRow("SELECT state FROM content_journals").Scan(&state)
	if state != "blocked" {
		t.Fatal(state)
	}
}
func TestEditorCASRolePathsAndTextPreservation(t *testing.T) {
	f := setup(t, false)
	os.MkdirAll(filepath.Join(f.dirs.Server, "config"), 0700)
	file := filepath.Join(f.dirs.Server, "config/example.toml")
	os.WriteFile(file, []byte("# comments\nkey=old\npassword=secret-canary\n"), 0600)
	read, e := f.s.ConfigRead(ctx, "alpha", "config/example.toml", operator)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.ConfigRead(ctx, "alpha", read.Path, domain.User{ID: 3}); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
	next := strings.Replace(read.Text, "key=old", "key=new", 1)
	p := f.preview(t, Mutation{Action: "config_write", ConfigPath: read.Path, ExpectedSHA256: read.SHA256, Text: next}, operator)
	if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	b, _ := os.ReadFile(file)
	if string(b) != next {
		t.Fatal("whole text was not preserved")
	}
	if _, e = f.s.Preview(ctx, "alpha", Mutation{Action: "config_write", ConfigPath: read.Path, ExpectedSHA256: read.SHA256, Text: "stale"}, operator); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	for _, name := range []string{"../server.properties", "config/../server.properties", "server.properties", "config/.hidden.toml", "plugins/top.yml", "config/world/secret.json", "config/rcon.properties", "config/a.jar", "config/a\\b.toml", "config/CON.txt"} {
		if _, e = f.s.ConfigRead(ctx, "alpha", name, operator); e == nil {
			t.Fatal("unsafe config accepted", name)
		}
	}
	os.WriteFile(file, bytes.Repeat([]byte("x"), MaxConfigBytes+1), 0600)
	if _, e = f.s.ConfigRead(ctx, "alpha", read.Path, operator); e == nil {
		t.Fatal("oversized config read")
	}
	os.WriteFile(file, []byte{0, 1, 2}, 0600)
	if _, e = f.s.ConfigRead(ctx, "alpha", read.Path, operator); e == nil {
		t.Fatal("binary config read")
	}
}
func TestEditorRefusesSymlinksAndHardlinks(t *testing.T) {
	f := setup(t, false)
	os.MkdirAll(filepath.Join(f.dirs.Server, "config"), 0700)
	outside := filepath.Join(t.TempDir(), "outside.toml")
	os.WriteFile(outside, []byte("secret"), 0600)
	for _, link := range []string{"soft.toml", "hard.toml"} {
		name := filepath.Join(f.dirs.Server, "config", link)
		var e error
		if link == "soft.toml" {
			e = os.Symlink(outside, name)
		} else {
			e = os.Link(outside, name)
		}
		if e != nil {
			t.Skip(e)
		}
		if _, e = f.s.ConfigRead(ctx, "alpha", "config/"+link, operator); e == nil {
			t.Fatal("linked config accepted", link)
		}
	}
}
func TestDescriptorsRejectTraversalClientAndUnresolvedDependencies(t *testing.T) {
	for name, b := range map[string][]byte{"traversal": jarEntries(t, map[string]string{"../evil": "bad"}), "oversized": jarEntries(t, map[string]string{"fabric.mod.json": strings.Repeat("x", 256<<10+1)}), "duplicates": jarEntries(t, map[string]string{"a": "one", "A": "two"})} {
		if _, e := inspectJar(b); e == nil {
			t.Fatal("unsafe descriptor accepted", name)
		}
	}
	f := setup(t, false)
	for name, descriptor := range map[string]string{"client": `{"id":"client","version":"1","environment":"client"}`, "wrongloader": `{"id":"forge","version":"1","depends":{"minecraft":"1.21.1","fabricloader":"0.16.5"}}`, "dependency": `{"id":"needs","version":"1","environment":"server","depends":{"minecraft":"1.21.1","fabricloader":"0.16.5","missing":"*"}}`} {
		b := jar(t, descriptor)
		if name == "wrongloader" {
			b = jarEntries(t, map[string]string{"plugin.yml": "name: Plugin\nversion: '1'\napi-version: '1.21'\n"})
		}
		u := f.upload(t, name+".jar", b)
		p, e := f.s.Preview(ctx, "alpha", Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
		if e == nil && len(p.Blockers) == 0 {
			t.Fatal("unsafe compatibility accepted", name)
		}
	}
}
func TestOperatorCatalogInstallAndVerifiedManualResume(t *testing.T) {
	f := setup(t, false)
	data := jar(t, fabric("verified", "1.0.0"))
	file := filepath.Join(t.TempDir(), "verified.artifact")
	os.WriteFile(file, data, 0600)
	f.catalog.path = file
	selection := catalog.Selection{Provider: catalog.Modrinth, ProjectID: "project", VersionID: "version", FileID: "file"}
	f.catalog.version = catalog.Version{ID: "version", ProjectID: "project", Compatibility: catalog.Compatibility{Minecraft: []string{"1.21.1"}, Loaders: []string{"fabric"}, ServerSide: "supported", Environment: "server"}, Files: []catalog.File{{ID: "file", Filename: "verified.jar", Size: int64(len(data)), URL: "https://cdn.modrinth.com/file", Hashes: map[string]string{"sha256": digest(data)}}}}
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, Selection: &selection}, operator)
	if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	m, _ := f.s.Snapshot(ctx, "alpha")
	if m.Items[0].Unknown || m.Items[0].Selection == nil {
		t.Fatal("verified provenance lost")
	}
	p = f.preview(t, Mutation{Action: "uninstall", ContentID: m.Items[0].ID}, operator)
	f.apply(t, p, operator)
	f.catalog.version.Files[0].URL = ""
	f.catalog.version.Files[0].ManualReason = "Publisher withheld direct download"
	p = f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, Selection: &selection}, operator)
	if p.Manual == nil || len(p.Blockers) == 0 {
		t.Fatal("manual resolution not retained")
	}
	wrong := f.upload(t, "wrong.jar", jar(t, fabric("verified", "9.0.0")))
	if _, e := f.s.Preview(ctx, "alpha", Mutation{Action: "install", Kind: catalog.Mod, Selection: &selection, UploadID: wrong.ID}, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("wrong manual digest accepted", e)
	}
	correct := f.upload(t, "manual.jar", data)
	p = f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, Selection: &selection, UploadID: correct.ID}, admin)
	if j := f.apply(t, p, admin); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	if f.catalog.downloads != 1 {
		t.Fatal("manual resume downloaded untrusted URL")
	}
}

func TestOpaqueUnknownTrialCanDisableAndEnableOnlyAtItsTuple(t *testing.T) {
	f := setup(t, false)
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	if j := f.apply(t, p, admin); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	m, _ := f.s.Snapshot(ctx, "alpha")
	item := m.Items[0]
	for _, action := range []string{"disable", "enable", "disable"} {
		p = f.preview(t, Mutation{Action: action, ContentID: item.ID}, operator)
		if j := f.apply(t, p, operator); j.State != domain.JobSucceeded {
			t.Fatal(j)
		}
	}
	if _, e := f.db.Exec("UPDATE instances SET loader_version='0.16.6' WHERE id='alpha'"); e != nil {
		t.Fatal(e)
	}
	p = f.preview(t, Mutation{Action: "enable", ContentID: item.ID}, operator)
	if len(p.Blockers) == 0 {
		t.Fatal("unknown trial enabled on another tuple")
	}
}
func TestDiscardPreviewPreservesNewManualUpload(t *testing.T) {
	f := setup(t, false)
	u := f.upload(t, "first.jar", jar(t, fabric("first", "1.0.0")))
	p := f.preview(t, Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin)
	second := f.upload(t, "second.jar", jar(t, fabric("second", "1.0.0")))
	if e := f.s.Discard(ctx, "alpha", p.ID, admin); e != nil {
		t.Fatal(e)
	}
	if _, reader, e := f.s.openStage(ctx, "alpha", second.ID); e != nil {
		t.Fatal("discard destroyed a newly staged upload", e)
	} else {
		reader.Close()
	}
}
func TestUploadAndPreviewBoundsAndImmutability(t *testing.T) {
	f := setup(t, false)
	u := f.upload(t, "immutable.jar", jar(t, fabric("immutable", "1.0.0")))
	stage := filepath.Join(f.dirs.State, "content", u.ID+".blob")
	os.Chmod(stage, 0600)
	os.WriteFile(stage, []byte("changed"), 0600)
	if _, reader, e := f.s.openStage(ctx, "alpha", u.ID); e == nil {
		reader.Close()
		t.Fatal("changed immutable stage accepted")
	}
	for _, name := range []string{"../world.zip", "world.zip", "CON.jar", "bad:jar.jar", "trailing.jar.", "mods/a.jar"} {
		if _, e := f.s.StageUpload(ctx, "alpha", name, bytes.NewReader(nil), admin); e == nil {
			t.Fatal("unsafe upload name", name)
		}
	}
	if _, e := f.s.StageUpload(ctx, "alpha", "op.jar", bytes.NewReader(jar(t, fabric("op", "1.0.0"))), operator); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
	// Reserve full quota with a crash-style metadata reservation; no huge allocation.
	f.db.Exec("INSERT INTO content_stages(id,instance_id,actor_id,filename,sha256,size,created_at) VALUES('quota','alpha',1,'quota','',8589934592,'2099-01-01T00:00:00Z')")
	if _, e := f.s.StageUpload(ctx, "alpha", "quota.jar", bytes.NewReader(jar(t, fabric("quota", "1.0.0"))), admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("staging quota bypassed", e)
	}
}
func TestSnapshotBlocksUnmanagedAndLocallyModifiedContent(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	os.WriteFile(filepath.Join(f.dirs.Server, "mods/example.jar"), []byte("modified"), 0600)
	os.WriteFile(filepath.Join(f.dirs.Server, "mods/unmanaged.jar"), []byte("unmanaged"), 0600)
	m, e := f.s.Snapshot(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(m.AutomaticBlockers, " ")
	if !strings.Contains(joined, "local changes") || !strings.Contains(joined, "Unmanaged executable") || !strings.Contains(joined, item.ID) {
		t.Fatal(m.AutomaticBlockers)
	}
}
func TestCommittedRecoveryDoesNotRollBackManifestOrLocalEdits(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	f.s.fault = func(phase string) error {
		if phase == "commit" {
			return errors.New("manager exit after commit")
		}
		return nil
	}
	if j := f.apply(t, p, operator); j.State != domain.JobFailed {
		t.Fatal(j)
	}
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	disabled := filepath.Join(f.dirs.Server, "mods/example.jar.disabled")
	os.WriteFile(disabled, []byte("postcommit edit"), 0600)
	if e = f.s.recoverJournal(ctx, root, "alpha"); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	var state string
	f.db.QueryRow("SELECT state FROM content_journals").Scan(&state)
	if state != "committed" {
		t.Fatal("committed recovery was converted into rollback", state)
	}
	m, _ := f.s.Snapshot(ctx, "alpha")
	if m.Items[0].Enabled {
		t.Fatal("committed manifest rolled back")
	}
	b, _ := os.ReadFile(disabled)
	if string(b) != "postcommit edit" {
		t.Fatal("postcommit edit overwritten")
	}
}

func TestDurableJournalReconcilesAfterDatabaseReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "manager.db")
	f := setupDatabase(t, false, dbPath)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	private, e := f.s.loadPreview(ctx, "alpha", p.ID, operator.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(private)
	if _, e = f.db.Exec("INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES('alpha',?,?,'applying')", p.ID, string(raw)); e != nil {
		t.Fatal(e)
	}
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	first := private.Operations[0]
	if e = atomicStream(ctx, root, first.Path, first.Before, nil, true); e != nil {
		t.Fatal(e)
	}
	root.Close()
	if e = f.q.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := db.Open(ctx, dbPath)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	q := jobs.New(reopened, nil)
	instances := instance.New(reopened, f.p, f.g, instance.WithJobMutationGuard(f.g))
	s, e := New(Config{DB: reopened, Paths: f.p, Instances: instances, Queue: q, Recovery: f.g})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Register(); e != nil {
		t.Fatal(e)
	}
	if e = q.StartPaused(ctx); e != nil {
		t.Fatal(e)
	}
	defer q.Stop(ctx)
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	restored, e := os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
	if e != nil || digest(restored) != item.SHA256 {
		t.Fatal("durable stage preimage not restored", e)
	}
	m, e := s.Snapshot(ctx, "alpha")
	if e != nil || !m.Items[0].Enabled {
		t.Fatal(m, e)
	}
	var journals int
	reopened.QueryRow("SELECT count(*) FROM content_journals").Scan(&journals)
	if journals != 0 {
		t.Fatal("reconciled journal retained")
	}
}
func TestParentReplacementAfterPublicationDoesNotCommit(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	moved := f.dirs.Server + "-moved"
	once := false
	f.s.fault = func(phase string) error {
		if phase == "file" && !once {
			once = true
			if e := os.Rename(f.dirs.Server, moved); e != nil {
				return e
			}
			if e := os.Mkdir(f.dirs.Server, 0700); e != nil {
				return e
			}
		}
		return nil
	}
	j := f.apply(t, p, operator)
	if j.State != domain.JobFailed {
		t.Fatal("substituted canonical server committed", j)
	}
	var raw string
	f.db.QueryRow("SELECT manifest_json FROM content_items WHERE id=?", item.ID).Scan(&raw)
	var installed Installed
	json.Unmarshal([]byte(raw), &installed)
	if !installed.Enabled {
		t.Fatal("manifest changed on detached server publication")
	}
	b, e := os.ReadFile(filepath.Join(moved, "mods/example.jar"))
	if e != nil || digest(b) != item.SHA256 {
		t.Fatal("original anchored preimage was not restored", e)
	}
}
func TestArchiveLinksAreNeverExtractedOrTrusted(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	header := &zip.FileHeader{Name: "linked"}
	header.SetMode(os.ModeSymlink | 0777)
	f, e := z.CreateHeader(header)
	if e != nil {
		t.Fatal(e)
	}
	f.Write([]byte("../../world"))
	z.Close()
	if _, e = inspectJar(b.Bytes()); e == nil {
		t.Fatal("symlink descriptor archive accepted")
	}
}

func TestDescriptorFamiliesAndVersionConstraints(t *testing.T) {
	for name, entries := range map[string]map[string]string{
		"paper":    {"plugin.yml": "name: Example\nversion: '1.0.0'\napi-version: '1.21'\ndepend: [Dependency]\n"},
		"forge":    {"META-INF/mods.toml": "modLoader=\"javafml\"\nloaderVersion=\"[47,)\"\n[[mods]]\nmodId=\"example\"\nversion=\"${file.jarVersion}\"\n[[dependencies.example]]\nmodId=\"minecraft\"\nmandatory=true\nversionRange=\"[1.20.1,1.21)\"\n[[dependencies.example]]\nmodId=\"forge\"\nmandatory=true\nversionRange=\"[47,)\"\n", "META-INF/MANIFEST.MF": "Manifest-Version: 1.0\r\nImplementation-Version: 1.0.0\r\n"},
		"neoforge": {"META-INF/neoforge.mods.toml": "modLoader=\"javafml\"\n[[mods]]\nmodId=\"example\"\nversion=\"1.0.0\"\n[[dependencies.example]]\nmodId=\"minecraft\"\ntype=\"required\"\nversionRange=\"[1.21.1]\"\n"},
	} {
		d, e := inspectJar(jarEntries(t, entries))
		if e != nil || d.Unknown || d.Loader != name || d.Version != "1.0.0" {
			t.Fatal(name, d, e)
		}
		if name == "forge" {
			if d.LanguageRequirement != "[47,)" {
				t.Fatal("FML constraint discarded", d)
			}
			if len(enabledManifestBlockers(Tuple{Minecraft: "1.20.1", Loader: "forge", LoaderVersion: "47.4.0"}, []Installed{{ID: "forge-item", Kind: catalog.Mod, Enabled: true, Descriptor: d}})) == 0 {
				t.Fatal("unverified FML language requirement accepted")
			}
		}
	}
	for _, test := range []struct {
		version, requirement string
		want                 bool
	}{{"1.21.1", "[1.21,1.22)", true}, {"0.16.5", ">=0.16.0 <0.17.0", true}, {"1.20.1", "[1.20.1]", true}, {"1.21.1", "[1.20,1.21)", false}, {"1.21.1", "~1.20", false}, {"unknown", ">=1", false}} {
		if got := satisfies(test.version, test.requirement); got != test.want {
			t.Fatal(test, got)
		}
	}
}

func TestContentDoesNotInstallLoaderOrBypassServerProvisioning(t *testing.T) {
	f := setup(t, false)
	if _, e := f.db.Exec("UPDATE instance_settings SET provisioning='not_installed' WHERE instance_id='alpha'"); e != nil {
		t.Fatal(e)
	}
	u := f.upload(t, "opaque.jar", jarEntries(t, map[string]string{"Opaque.class": "inert"}))
	if _, e := f.s.Preview(ctx, "alpha", Mutation{Action: "install", Kind: catalog.Mod, UploadID: u.ID, Trial: true}, admin); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("content activated without installed loader", e)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods/opaque.jar")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}

func TestConfigListingDisclosesBoundAndOmitsReservedAreas(t *testing.T) {
	f := setup(t, false)
	config := filepath.Join(f.dirs.Server, "config")
	os.MkdirAll(filepath.Join(config, "world"), 0700)
	os.WriteFile(filepath.Join(config, "world/secret.toml"), []byte("hidden"), 0600)
	os.WriteFile(filepath.Join(config, ".hidden.toml"), []byte("hidden"), 0600)
	for i := 0; i < 1005; i++ {
		name := fmt.Sprintf("entry-%04d.toml", i)
		if e := os.WriteFile(filepath.Join(config, name), []byte("value=true"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	listing, e := f.s.ConfigList(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	if !listing.Truncated || len(listing.Files) > 1000 {
		t.Fatal(listing.Truncated, len(listing.Files))
	}
	for _, file := range listing.Files {
		if strings.Contains(file.Path, "hidden") || strings.Contains(file.Path, "/world/") {
			t.Fatal("reserved config metadata exposed", file)
		}
	}
}

func TestSQLIntentAndOrphanMarkerGateOrdinaryMutations(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	private, e := f.s.loadPreview(ctx, "alpha", p.ID, operator.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(private)
	if _, e = f.db.Exec("INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES('alpha',?,?,'applying')", p.ID, string(raw)); e != nil {
		t.Fatal(e)
	}
	if e = f.s.CheckReady(ctx, "alpha"); e == nil {
		t.Fatal("SQL intent without marker admitted mutation")
	}
	if _, e = f.s.Enqueue(ctx, "alpha", p.ID, operator); e == nil {
		t.Fatal("pending intent admitted content apply")
	}
	if e = f.s.markTransaction(ctx, "alpha", p.ID); e != nil {
		t.Fatal(e)
	}
	dataDir := filepath.Dir(filepath.Dir(f.dirs.Root))
	if e = launcher.CheckActivationReady(ctx, dataDir, "alpha"); domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatal("independent launcher crossed marker", e)
	}
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.s.recoverJournal(ctx, root, "alpha"); e != nil {
		t.Fatal(e)
	}
	root.Close()
	if e = f.s.CheckReady(ctx, "alpha"); e != nil {
		t.Fatal("verified rollback retained readiness block", e)
	}
	if e = launcher.CheckActivationReady(ctx, dataDir, "alpha"); e != nil {
		t.Fatal("verified rollback retained launch block", e)
	}
	if e = f.s.markTransaction(ctx, "alpha", p.ID); e != nil {
		t.Fatal(e)
	}
	if e = f.s.Reconcile(ctx); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("orphan boot blocker was silently cleared", e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.State, markerName)); e != nil {
		t.Fatal(e)
	}
	if e = f.s.CheckReady(ctx, "alpha"); e == nil {
		t.Fatal("orphan boot blocker admitted mutation")
	}
}
func TestFailedRecoveryRetainsBootBlockerAndPreimages(t *testing.T) {
	f := setup(t, false)
	item := f.installUnknown(t)
	p := f.preview(t, Mutation{Action: "disable", ContentID: item.ID}, operator)
	private, e := f.s.loadPreview(ctx, "alpha", p.ID, operator.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(private)
	f.db.Exec("INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES('alpha',?,?,'applying')", p.ID, string(raw))
	if e = f.s.markTransaction(ctx, "alpha", p.ID); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(f.dirs.Server, "mods/example.jar"), []byte("competing edit"), 0600)
	root, e := f.s.openServer(ctx, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if e = f.s.recoverJournal(ctx, root, "alpha"); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.State, markerName)); e != nil {
		t.Fatal("failed recovery removed boot blocker", e)
	}
	if e = f.s.CheckReady(ctx, "alpha"); e == nil {
		t.Fatal("failed recovery admitted ordinary mutation")
	}
	if e = launcher.CheckActivationReady(ctx, filepath.Dir(filepath.Dir(f.dirs.Root)), "alpha"); e == nil {
		t.Fatal("failed recovery admitted launcher")
	}
	if _, file, e := f.s.openStage(ctx, "alpha", private.Operations[0].BeforeStage); e != nil {
		t.Fatal("failed recovery removed immutable preimage", e)
	} else {
		file.Close()
	}
}
