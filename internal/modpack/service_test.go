package modpack

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var packAdmin = domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}

type packGuard struct {
	mu    sync.Mutex
	calls int
}

func (g *packGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return fn()
}
func (g *packGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error { return g.WithStopped(ctx, id, func() error { return fn(id) }) })
}
func (g *packGuard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.WithStopped(ctx, id, fn)
}

type packCatalog struct {
	projectID     string
	versions      map[string]catalog.Version
	downloadError error
	artifact      catalog.VerifiedArtifact
	version       catalog.Version
}

func (c *packCatalog) Project(context.Context, catalog.Provider, string) (catalog.Project, error) {
	id := c.projectID
	if id == "" {
		id = "pack"
	}
	return catalog.Project{ID: id, Kind: catalog.Modpack}, nil
}
func (c *packCatalog) Version(_ context.Context, _ catalog.Provider, _ string, version string) (catalog.Version, error) {
	if c.versions != nil {
		v, ok := c.versions[version]
		if !ok {
			return v, domain.NotFound("catalog version")
		}
		return v, nil
	}
	return c.version, nil
}
func (c *packCatalog) Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error) {
	return c.artifact, c.downloadError
}

type packFixture struct {
	s        *Service
	c        *packCatalog
	q        *jobs.Service
	database *sql.DB
	data     string
	dirs     instance.Directories
	guard    *packGuard
}

func newPackFixture(t *testing.T) *packFixture {
	t.Helper()
	data := t.TempDir()
	database, e := db.Open(t.Context(), filepath.Join(data, "manager.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	_, e = database.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now')")
	if e != nil {
		t.Fatal(e)
	}
	paths, e := instance.NewPaths(data)
	if e != nil {
		t.Fatal(e)
	}
	guard := &packGuard{}
	instances := instance.New(database, paths, guard, instance.WithJobMutationGuard(guard))
	_, e = instances.Create(t.Context(), packAdmin, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.0", JavaMajor: 17, MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if e != nil {
		t.Fatal(e)
	}
	_, e = database.Exec("UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'")
	if e != nil {
		t.Fatal(e)
	}
	q := jobs.New(database, nil)
	c := &packCatalog{}
	cs, e := content.New(content.Config{DB: database, Paths: paths, Instances: instances, Queue: q, Recovery: guard})
	if e != nil {
		t.Fatal(e)
	}
	if e = cs.Register(); e != nil {
		t.Fatal(e)
	}
	s, e := New(Config{DB: database, Paths: paths, Instances: instances, Queue: q, Catalog: c, Content: cs, Recovery: guard})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Register(); e != nil {
		t.Fatal(e)
	}
	if e = q.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { q.Stop(context.Background()) })
	dirs, _ := paths.For("alpha")
	return &packFixture{s: s, c: c, q: q, database: database, data: data, dirs: dirs, guard: guard}
}
func (f *packFixture) setPack(t *testing.T, version string, entries map[string][]byte) {
	t.Helper()
	entries["modrinth.index.json"] = mrIndex()
	b := packZip(t, entries)
	p := filepath.Join(t.TempDir(), "pack.mrpack")
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	selection := catalog.Selection{Provider: catalog.Modrinth, ProjectID: "pack", VersionID: version, FileID: "file"}
	f.c.artifact = catalog.VerifiedArtifact{Selection: selection, Provenance: catalog.Provenance{Source: catalog.Modrinth, FetchedAt: time.Now().UTC()}, File: catalog.File{ID: "file", Filename: "pack.mrpack", Size: int64(len(b)), Hashes: map[string]string{"sha256": digest(b)}}, SHA256: digest(b), Path: p}
	f.c.version = catalog.Version{ID: version, ProjectID: "pack", Name: version, OfficialURL: "https://modrinth.com/modpack/pack/version/" + version, Files: []catalog.File{f.c.artifact.File}, Compatibility: catalog.Compatibility{Minecraft: []string{"1.20.1"}, Loaders: []string{"fabric"}, ServerSide: "supported"}}
}
func (f *packFixture) preview(t *testing.T, choices map[string]string) Preview {
	t.Helper()
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Choices: choices}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func (f *packFixture) apply(t *testing.T, p Preview) jobs.Job {
	t.Helper()
	j, e := f.s.Enqueue(t.Context(), "alpha", p.ID, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		j, e = f.q.Get(t.Context(), j.ID)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == domain.JobSucceeded || j.State == domain.JobFailed || j.State == domain.JobCancelled {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job timed out")
	return j
}
func TestPackThreeWayApplyAndPreservedEditedConfig(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old"), "overrides/config/deleted.cfg": []byte("remove")})
	p := f.preview(t, nil)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	if e := os.WriteFile(filepath.Join(f.dirs.Server, "config", "a.cfg"), []byte("local"), 0600); e != nil {
		t.Fatal(e)
	}
	f.setPack(t, "two", map[string][]byte{"overrides/config/a.cfg": []byte("new")})
	p = f.preview(t, nil)
	if len(p.Blockers) == 0 {
		t.Fatal("missing local-edit conflict")
	}
	p = f.preview(t, map[string]string{"config/a.cfg": "keep"})
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	b, e := os.ReadFile(filepath.Join(f.dirs.Server, "config", "a.cfg"))
	if e != nil || string(b) != "local" {
		t.Fatal(string(b), e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "config", "deleted.cfg")); !os.IsNotExist(e) {
		t.Fatal("upstream removal not applied", e)
	}
	snapshot, e := f.s.Snapshot(t.Context(), "alpha")
	if e != nil || snapshot.Baseline.Source.Selection.VersionID != "two" {
		t.Fatal(snapshot, e)
	}
}
func TestInterruptedPackApplyRollsBackOnlyOwnedFiles(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("a"), "overrides/config/b.cfg": []byte("b")})
	p := f.preview(t, nil)
	f.s.fault = func(at string) error {
		if at == "file" {
			return errors.New("power loss")
		}
		return nil
	}
	if j := f.apply(t, p); j.State != domain.JobFailed {
		t.Fatal(j)
	}
	if e := launcher.CheckActivationReady(t.Context(), f.data, "alpha"); e == nil {
		t.Fatal("interrupted pack admitted boot")
	}
	f.s.fault = nil
	if e := f.s.Reconcile(t.Context()); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"a.cfg", "b.cfg"} {
		if _, e := os.Stat(filepath.Join(f.dirs.Server, "config", name)); !os.IsNotExist(e) {
			t.Fatal("partial pack survived rollback", e)
		}
	}
	if e := launcher.CheckActivationReady(t.Context(), f.data, "alpha"); e != nil {
		t.Fatal(e)
	}
}
func TestInterruptedPackRecoveryPreservesExternalEdit(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("a"), "overrides/config/b.cfg": []byte("b")})
	p := f.preview(t, nil)
	f.s.fault = func(at string) error {
		if at == "file" {
			return errors.New("power loss")
		}
		return nil
	}
	f.apply(t, p)
	if e := os.WriteFile(filepath.Join(f.dirs.Server, "config", "a.cfg"), []byte("external"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Reconcile(t.Context()); e == nil {
		t.Fatal("external edit overwritten")
	}
	b, e := os.ReadFile(filepath.Join(f.dirs.Server, "config", "a.cfg"))
	if e != nil || string(b) != "external" {
		t.Fatal(string(b), e)
	}
}
func bundledJar(t *testing.T) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create("fabric.mod.json")
	f.Write([]byte(`{"id":"example","version":"1.0.0","environment":"server","depends":{"minecraft":"1.20.1","fabricloader":">=0.16.0"}}`))
	w.Close()
	return b.Bytes()
}
func TestBundledPackJarJoinsContentManifest(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/mods/example.jar": bundledJar(t)})
	p := f.preview(t, nil)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	var raw string
	if e := f.database.QueryRow("SELECT manifest_json FROM content_items WHERE instance_id='alpha'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var item content.Installed
	if e := json.Unmarshal([]byte(raw), &item); e != nil || item.PackMember == nil || item.PackMember.Path != "mods/example.jar" {
		t.Fatal(item, e)
	}
}
func TestManualRequiredConfigResumeIsDigestVerifiedAndActorBound(t *testing.T) {
	f := newPackFixture(t)
	entry := indexEntry("config/a.cfg", "required")
	body := []byte("jar")
	entry["downloads"] = []string{"https://unapproved.invalid/a.cfg"}
	b := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(entry)})
	packPath := filepath.Join(t.TempDir(), "manual.mrpack")
	os.WriteFile(packPath, b, 0600)
	f.setPack(t, "one", map[string][]byte{})
	f.c.artifact.Path = packPath
	f.c.artifact.File.Size = int64(len(b))
	f.c.artifact.SHA256 = digest(b)
	f.c.version.Files[0] = f.c.artifact.File
	p := f.preview(t, nil)
	if len(p.Manual) != 1 {
		t.Fatal(p)
	}
	if _, e := f.s.Enqueue(t.Context(), "alpha", p.ID, packAdmin); e == nil {
		t.Fatal("missing required bytes admitted")
	}
	if _, e := f.s.ResumeMember(t.Context(), "alpha", p.ID, "config/a.cfg", bytes.NewReader([]byte("bad")), packAdmin); e == nil {
		t.Fatal("wrong digest accepted")
	}
	p, e := f.s.ResumeMember(t.Context(), "alpha", p.ID, "config/a.cfg", bytes.NewReader(body), packAdmin)
	if e != nil || len(p.Manual) != 0 || len(p.Blockers) > 0 {
		t.Fatal(p, e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	actual, e := os.ReadFile(filepath.Join(f.dirs.Server, "config", "a.cfg"))
	if e != nil || string(actual) != "jar" {
		t.Fatal(string(actual), e)
	}
}
func TestMissingLocalFileRequiresExplicitUpstreamChoice(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old")})
	p := f.preview(t, nil)
	f.apply(t, p)
	os.Remove(filepath.Join(f.dirs.Server, "config", "a.cfg"))
	f.setPack(t, "two", map[string][]byte{"overrides/config/a.cfg": []byte("new")})
	p = f.preview(t, nil)
	if len(p.Blockers) == 0 {
		t.Fatal("missing file silently restored")
	}
	p = f.preview(t, map[string]string{"config/a.cfg": "upstream"})
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
}
func TestPinnedDisabledAndOptionalSelectionsSurvivePackUpdate(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/mods/example.jar": bundledJar(t), "overrides/config/a.cfg": []byte("old")})
	request := Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"mods/example.jar": {Selected: true, Disabled: true, Pinned: true}, "config/a.cfg": {Selected: true, Excluded: true}}}
	p, e := f.s.Preview(t.Context(), "alpha", request, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods", "example.jar.disabled")); !os.IsNotExist(e) {
		t.Fatal("pinned missing item unexpectedly installed", e)
	}
	f.setPack(t, "two", map[string][]byte{"overrides/mods/example.jar": bundledJar(t), "overrides/config/a.cfg": []byte("new")})
	p = f.preview(t, nil)
	if !p.Selections["mods/example.jar"].Disabled || !p.Selections["mods/example.jar"].Pinned || !p.Selections["config/a.cfg"].Excluded {
		t.Fatal(p.Selections)
	}
}
func TestPackCommitRechecksRevokedActorInsideSQLTransaction(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/mods/example.jar": bundledJar(t)})
	p := f.preview(t, nil)
	f.s.fault = func(at string) error {
		if at == "before_commit" {
			_, e := f.database.Exec("UPDATE users SET disabled=1 WHERE id=1")
			return e
		}
		return nil
	}
	if j := f.apply(t, p); j.State != domain.JobFailed {
		t.Fatal("revoked actor committed", j)
	}
	var count int
	f.database.QueryRow("SELECT count(*) FROM content_items WHERE instance_id='alpha'").Scan(&count)
	if count != 0 {
		t.Fatal("manifest committed despite revoked authority")
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods", "example.jar")); !os.IsNotExist(e) {
		t.Fatal("revoked publication not restored", e)
	}
}
func TestDisabledPackJarIsUpdatedWithoutEnablingIt(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/mods/example.jar": bundledJar(t)})
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"mods/example.jar": {Selected: true, Disabled: true}}}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	f.setPack(t, "two", map[string][]byte{"overrides/mods/example.jar": bundledJar(t), "overrides/config/new.cfg": []byte("new")})
	p = f.preview(t, nil)
	if !p.Selections["mods/example.jar"].Disabled {
		t.Fatal("disabled selection lost")
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods", "example.jar.disabled")); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "mods", "example.jar")); !os.IsNotExist(e) {
		t.Fatal("disabled content enabled", e)
	}
}
func TestBinaryAddonResourcesPublishByteForByte(t *testing.T) {
	f := newPackFixture(t)
	resource := []byte{0, 255, 128, 1}
	f.setPack(t, "one", map[string][]byte{"overrides/kubejs/assets/example/icon.png": resource, "overrides/kubejs/data/example/tower.nbt": resource})
	p := f.preview(t, nil)
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	for _, name := range []string{"kubejs/assets/example/icon.png", "kubejs/data/example/tower.nbt"} {
		actual, e := os.ReadFile(filepath.Join(f.dirs.Server, name))
		if e != nil || !bytes.Equal(actual, resource) {
			t.Fatal(name, actual, e)
		}
	}
}
func TestPackPreservesUnknownTrialProvenanceAndAutomaticBlocker(t *testing.T) {
	f := newPackFixture(t)
	cs := f.s.content.(*content.Service)
	upload, e := cs.StageUpload(t.Context(), "alpha", "trial.jar", bytes.NewReader(bundledJar(t)), packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	preview, e := cs.Preview(t.Context(), "alpha", content.Mutation{Action: "install", Kind: catalog.Mod, UploadID: upload.ID, Trial: true}, packAdmin)
	if e != nil || len(preview.Blockers) > 0 {
		t.Fatal(preview, e)
	}
	job, e := cs.Enqueue(t.Context(), "alpha", preview.ID, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		current, _ := f.q.Get(t.Context(), job.ID)
		if current.State == domain.JobSucceeded {
			break
		}
		if current.State == domain.JobFailed {
			t.Fatal(current)
		}
		time.Sleep(time.Millisecond)
	}
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("one")})
	p := f.preview(t, nil)
	if len(p.Blockers) > 0 {
		t.Fatal(p.Blockers)
	}
	if applied := f.apply(t, p); applied.State != domain.JobSucceeded {
		t.Fatal(applied)
	}
	snapshot, e := cs.Snapshot(t.Context(), "alpha")
	if e != nil || len(snapshot.Items) != 1 || !snapshot.Items[0].Unknown || snapshot.Items[0].TrialTuple == nil || len(snapshot.AutomaticBlockers) == 0 {
		t.Fatal(snapshot, e)
	}
}

func TestRecoveryReplayPreservesEditAfterPreflight(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("published")})
	p := f.preview(t, nil)
	f.s.fault = func(at string) error {
		if at == "file" {
			return errors.New("interrupted")
		}
		return nil
	}
	if j := f.apply(t, p); j.State != domain.JobFailed {
		t.Fatal(j)
	}
	edited := false
	f.s.fault = func(at string) error {
		if at == "recovery_preflight" {
			edited = true
			return os.WriteFile(filepath.Join(f.dirs.Server, "config/a.cfg"), []byte("external after preflight"), 0600)
		}
		return nil
	}
	if e := f.s.Reconcile(t.Context()); e == nil {
		t.Fatal("replay overwrote post-preflight edit")
	}
	if !edited {
		t.Fatal("recovery did not reach preflight seam")
	}
	b, e := os.ReadFile(filepath.Join(f.dirs.Server, "config/a.cfg"))
	if e != nil || string(b) != "external after preflight" {
		t.Fatal(string(b), e)
	}
	if e = launcher.CheckActivationReady(t.Context(), f.data, "alpha"); e == nil {
		t.Fatal("recovery conflict lost boot blocker")
	}
}
func TestInstalledJarPinSurvivesMetadataOnlyApply(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/mods/example.jar": bundledJar(t)})
	if j := f.apply(t, f.preview(t, nil)); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"mods/example.jar": {Selected: true, Pinned: true}}}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	manifest, e := f.s.content.Snapshot(t.Context(), "alpha")
	if e != nil || len(manifest.Items) != 1 || !manifest.Items[0].Pinned {
		t.Fatal("installed pin not committed", manifest, e)
	}
	p = f.preview(t, nil)
	if !p.Selections["mods/example.jar"].Pinned {
		t.Fatal("installed pin lost", p.Selections)
	}
}
func (f *packFixture) indexedPack(t *testing.T, name string, body []byte, server string) {
	t.Helper()
	f.setPack(t, "one", map[string][]byte{})
	entry := indexEntry(name, server)
	entry["fileSize"] = len(body)
	a := sha1.Sum(body)
	b := sha512.Sum512(body)
	entry["hashes"] = map[string]string{"sha1": hex.EncodeToString(a[:]), "sha512": hex.EncodeToString(b[:])}
	entry["downloads"] = []string{"https://unapproved.invalid/file"}
	archive := packZip(t, map[string][]byte{"modrinth.index.json": mrIndex(entry)})
	if e := os.WriteFile(f.c.artifact.Path, archive, 0600); e != nil {
		t.Fatal(e)
	}
	f.c.artifact.File.Size = int64(len(archive))
	f.c.artifact.File.Hashes = map[string]string{"sha256": digest(archive)}
	f.c.artifact.SHA256 = digest(archive)
	f.c.version.Files[0] = f.c.artifact.File
}
func TestInstalledOptionalDeselectionSurvivesNextPreview(t *testing.T) {
	f := newPackFixture(t)
	body := bundledJar(t)
	f.indexedPack(t, "mods/example.jar", body, "optional")
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"mods/example.jar": {Selected: true}}}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	p, e = f.s.ResumeMember(t.Context(), "alpha", p.ID, "mods/example.jar", bytes.NewReader(body), packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	p, e = f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"mods/example.jar": {Selected: false}}}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	p = f.preview(t, nil)
	if p.Selections["mods/example.jar"].Selected {
		t.Fatal("saved optional deselection reset by installed bytes", p.Selections)
	}
}
func TestManualMemberRejectsBinaryConfiguration(t *testing.T) {
	for _, name := range []string{"config/a.cfg", "kubejs/server_scripts/a.js", "kubejs/assets/example/icon.png.mcmeta"} {
		t.Run(name, func(t *testing.T) {
			f := newPackFixture(t)
			body := []byte{0, 255, 1}
			f.indexedPack(t, name, body, "required")
			p := f.preview(t, nil)
			if _, e := f.s.ResumeMember(t.Context(), "alpha", p.ID, name, bytes.NewReader(body), packAdmin); e == nil {
				t.Fatal("binary config accepted despite exact digest")
			}
		})
	}
}
func TestManualMemberRejectsOversizedConfiguration(t *testing.T) {
	f := newPackFixture(t)
	body := bytes.Repeat([]byte("x"), MaxConfigBytes+1)
	f.indexedPack(t, "config/a.cfg", body, "required")
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection}, packAdmin)
	if e == nil {
		if _, e = f.s.ResumeMember(t.Context(), "alpha", p.ID, "config/a.cfg", bytes.NewReader(body), packAdmin); e == nil {
			t.Fatal("oversized config accepted despite exact digest")
		}
	}
}

func customizationJSON(t *testing.T, f *packFixture) map[string]any {
	t.Helper()
	snapshot, e := f.s.Snapshot(t.Context(), "alpha")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	var value map[string]any
	if e = json.Unmarshal(raw, &value); e != nil {
		t.Fatal(e)
	}
	summary, ok := value["customizations"].(map[string]any)
	if !ok {
		t.Fatal("snapshot lacks current customization summary", string(raw))
	}
	return summary
}
func TestSnapshotListsCurrentEditedDeletedAndSelectionCustomizations(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old"), "overrides/config/deleted.cfg": []byte("old")})
	if j := f.apply(t, f.preview(t, nil)); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	if e := os.WriteFile(filepath.Join(f.dirs.Server, "config/a.cfg"), []byte("edited"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(f.dirs.Server, "config/deleted.cfg")); e != nil {
		t.Fatal(e)
	}
	summary := customizationJSON(t, f)
	if summary["complete"] != true || summary["observed_at"] == "" || summary["source"].(map[string]any)["sha256"] != f.c.artifact.SHA256 {
		t.Fatal(summary)
	}
	entries := summary["entries"].([]any)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
	for i, reason := range []string{"edited", "deleted"} {
		entry := entries[i].(map[string]any)
		if entry["reasons"].([]any)[0] != reason || entry["evidence"] != "observed_file" {
			t.Fatal(entry)
		}
	}
}
func TestSnapshotBoundsHashWorkAndPrioritizesEditableConfig(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old")})
	if j := f.apply(t, f.preview(t, nil)); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	var raw string
	if e := f.database.QueryRow("SELECT baseline_json FROM modpack_baselines WHERE instance_id='alpha'").Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var baseline Baseline
	json.Unmarshal([]byte(raw), &baseline)
	baseline.Files["mods/large.jar"] = digest([]byte("old"))
	updated, _ := json.Marshal(baseline)
	if _, e := f.database.Exec("UPDATE modpack_baselines SET baseline_json=? WHERE instance_id='alpha'", string(updated)); e != nil {
		t.Fatal(e)
	}
	if e := os.MkdirAll(filepath.Join(f.dirs.Server, "mods"), 0700); e != nil {
		t.Fatal(e)
	}
	large, e := os.Create(filepath.Join(f.dirs.Server, "mods/large.jar"))
	if e != nil {
		t.Fatal(e)
	}
	e = large.Truncate(9 << 20)
	large.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(f.dirs.Server, "config/a.cfg"), []byte("edited"), 0600); e != nil {
		t.Fatal(e)
	}
	summary := customizationJSON(t, f)
	if summary["complete"] != false || summary["checked_files"] != float64(1) || summary["hashed_bytes"].(float64) > 16<<20 || len(summary["notices"].([]any)) == 0 {
		t.Fatal(summary)
	}
	entries := summary["entries"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["path"] != "config/a.cfg" {
		t.Fatal(entries)
	}
}

func TestSnapshotSavedChoicesRemainAvailableWhenScanBusy(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old")})
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, Selections: map[string]Selection{"config/a.cfg": {Selected: true, Excluded: true, Pinned: true}}}, packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	summaryScanSlots <- struct{}{}
	summaryScanSlots <- struct{}{}
	defer func() { <-summaryScanSlots; <-summaryScanSlots }()
	summary := customizationJSON(t, f)
	if summary["complete"] != false || summary["checked_files"] != float64(0) {
		t.Fatal(summary)
	}
	entries := summary["entries"].([]any)
	if len(entries) != 1 {
		t.Fatal(entries)
	}
	entry := entries[0].(map[string]any)
	if entry["evidence"] != "saved_selection" || entry["path"] != "config/a.cfg" || len(entry["reasons"].([]any)) != 2 {
		t.Fatal(entry)
	}
}
func TestSnapshotCacheDisclosesObservationTimeAndRefreshes(t *testing.T) {
	f := newPackFixture(t)
	f.setPack(t, "one", map[string][]byte{"overrides/config/a.cfg": []byte("old")})
	if j := f.apply(t, f.preview(t, nil)); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	first := customizationJSON(t, f)
	if e := os.WriteFile(filepath.Join(f.dirs.Server, "config/a.cfg"), []byte("edited"), 0600); e != nil {
		t.Fatal(e)
	}
	cached := customizationJSON(t, f)
	if cached["observed_at"] != first["observed_at"] || len(cached["entries"].([]any)) != 0 {
		t.Fatal("cache should retain its observation provenance", cached)
	}
	f.s.summaryMu.Lock()
	entry := f.s.summaryCache["alpha"]
	entry.expires = time.Time{}
	f.s.summaryCache["alpha"] = entry
	f.s.summaryMu.Unlock()
	fresh := customizationJSON(t, f)
	if fresh["observed_at"] == first["observed_at"] || len(fresh["entries"].([]any)) != 1 {
		t.Fatal("expired cache did not observe current edit", fresh)
	}
}

func TestManualBinaryResourceExceptionsAndReusedTextReceipt(t *testing.T) {
	for _, name := range []string{"kubejs/assets/example/icon.png", "kubejs/data/example/structure.nbt", "config/a.cfg"} {
		t.Run(name, func(t *testing.T) {
			f := newPackFixture(t)
			body := []byte{0, 255, 1}
			f.indexedPack(t, name, body, "required")
			p := f.preview(t, nil)
			if name == "config/a.cfg" {
				f.s.mu.Lock()
				st, e := f.s.stage(t.Context(), "alpha", packAdmin.ID, bytes.NewReader(body), int64(len(body)))
				f.s.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
				next, e := f.s.Preview(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection, PreviousPreviewID: p.ID, Manual: map[string]string{name: st.ID}}, packAdmin)
				if e == nil && len(next.Manual) == 0 {
					t.Fatal("reused receipt bypassed config text validation", next)
				}
				return
			}
			p, e := f.s.ResumeMember(t.Context(), "alpha", p.ID, name, bytes.NewReader(body), packAdmin)
			if e != nil || len(p.Manual) > 0 {
				t.Fatal(p, e)
			}
			if j := f.apply(t, p); j.State != domain.JobSucceeded {
				t.Fatal(j)
			}
			b, e := os.ReadFile(filepath.Join(f.dirs.Server, name))
			if e != nil || !bytes.Equal(b, body) {
				t.Fatal(b, e)
			}
		})
	}
}
func TestMemberManualApplyRejectsAdminDemotionAtCommit(t *testing.T) {
	f := newPackFixture(t)
	body := []byte("exact config")
	f.indexedPack(t, "config/a.cfg", body, "required")
	p := f.preview(t, nil)
	p, e := f.s.ResumeMember(t.Context(), "alpha", p.ID, "config/a.cfg", bytes.NewReader(body), packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.database.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'remaining-admin','admin','now','now')"); e != nil {
		t.Fatal(e)
	}
	demoted := false
	f.s.fault = func(at string) error {
		if at == "before_commit" {
			_, err := f.database.Exec("UPDATE users SET role='operator' WHERE id=1")
			demoted = err == nil
			return err
		}
		return nil
	}
	if j := f.apply(t, p); j.State != domain.JobFailed {
		t.Fatal("admin-only member committed after demotion", j)
	}
	if !demoted {
		t.Fatal("did not reach demotion seam")
	}
	var n int
	if e = f.database.QueryRow("SELECT count(*) FROM modpack_baselines WHERE instance_id='alpha'").Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "config/a.cfg")); !os.IsNotExist(e) {
		t.Fatal("publication not rolled back", e)
	}
}
