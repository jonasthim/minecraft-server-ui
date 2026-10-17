package update

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func coordinatorModJar(t *testing.T, version string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("fabric.mod.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schemaVersion": 1, "id": "example", "version": version, "environment": "server", "depends": map[string]string{"minecraft": ">=1.20.1 <1.20.3", "fabricloader": ">=0.16.0", "java": ">=17"}})
	if _, err = file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func manualContentFixture(t *testing.T) (*coordinatorFixture, Request, []byte) {
	t.Helper()
	f := newCoordinatorFixtureFlavor(t, domain.FlavorFabric)
	old := coordinatorModJar(t, "1.0")
	newBytes := coordinatorModJar(t, "2.0")
	os.MkdirAll(filepath.Join(f.dirs.Server, "mods"), 0700)
	os.WriteFile(filepath.Join(f.dirs.Server, "mods/example.jar"), old, 0600)
	descriptor, err := content.InspectPackReader(t.Context(), bytes.NewReader(old), int64(len(old)))
	if err != nil {
		t.Fatal(err)
	}
	oldSelection := catalog.Selection{Provider: catalog.Modrinth, ProjectID: "project", VersionID: "v1", FileID: "file1"}
	item := content.Installed{ID: "example", Kind: catalog.Mod, Name: "Example", Version: "1.0", Selection: &oldSelection, Enabled: true, SHA256: hashBytes(old), Files: []content.OwnedFile{{Path: "mods/example.jar", SHA256: hashBytes(old)}}, Descriptor: descriptor, Dependencies: []catalog.Dependency{}}
	raw, _ := json.Marshal(item)
	if _, err = f.s.config.DB.Exec("INSERT INTO content_items(instance_id,id,manifest_json)VALUES('alpha','example',?)", string(raw)); err != nil {
		t.Fatal(err)
	}
	*f.contentVersion = catalog.Version{ID: "v2", ProjectID: "project", Compatibility: catalog.Compatibility{Minecraft: []string{"1.20.2"}, Loaders: []string{"fabric"}, ServerSide: "supported", Environment: "server"}, Files: []catalog.File{{ID: "file2", Filename: "example.jar", Size: int64(len(newBytes)), Hashes: map[string]string{"sha256": hashBytes(newBytes)}, OfficialURL: "https://modrinth.com/mod/example/version/v2", ManualReason: "Publisher withheld automated download"}}}
	target := domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.2", LoaderVersion: "0.16.0"}
	request := Request{Mode: Manual, Selection: &target, ContentSelections: map[string]catalog.Selection{"example": {Provider: catalog.Modrinth, ProjectID: "project", VersionID: "v2", FileID: "file2"}}}
	return f, request, newBytes
}
func (f *coordinatorFixture) previewRequest(t *testing.T, request Request) Plan {
	t.Helper()
	job, err := f.s.Preview(t.Context(), "alpha", request, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		var last string
		f.s.config.DB.QueryRow("SELECT message FROM job_logs WHERE job_id=? ORDER BY id DESC LIMIT 1", job.ID).Scan(&last)
		t.Fatal(done, last)
	}
	plans, err := f.s.ListPreviews(t.Context(), "alpha", f.actor)
	if err != nil || len(plans) == 0 {
		t.Fatal(plans, err)
	}
	return plans[0]
}
func TestStandaloneManualTargetContentVerifiedResumeAndRetention(t *testing.T) {
	f, request, body := manualContentFixture(t)
	first := f.previewRequest(t, request)
	pending, ok := first.ContentManualRequired["example"]
	if !ok || pending.Selection.FileID != "file2" || len(first.Blockers) == 0 {
		t.Fatal(first.ContentManualRequired, first.Blockers)
	}
	upload, err := f.s.config.Content.StageUpload(t.Context(), "alpha", "example.jar", bytes.NewReader(body), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentManual = map[string]string{"example": upload.ID}
	ready := f.previewRequest(t, request)
	if len(ready.Blockers) > 0 || len(ready.ContentManualRequired) > 0 {
		t.Fatal(ready.Blockers, ready.ContentManualRequired)
	}
	// Ordinary preview cleanup must not delete a receipt referenced by a typed
	// update preview, including between a completed stage job and apply.
	if err = f.s.config.Content.Discard(t.Context(), "alpha", "", f.actor); err != nil {
		t.Fatal(err)
	}
	ready = f.previewRequest(t, request)
	job, err := f.s.EnqueueApply(t.Context(), "alpha", ready.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State != domain.JobSucceeded {
		var last string
		f.s.config.DB.QueryRow("SELECT message FROM job_logs WHERE job_id=? ORDER BY id DESC LIMIT 1", job.ID).Scan(&last)
		t.Fatal(done, last)
	}
	actual, _ := os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
	if !bytes.Equal(actual, body) {
		t.Fatal("verified publisher bytes not selected")
	}
	manifest, _ := f.s.config.Content.Snapshot(t.Context(), "alpha")
	if len(manifest.Items) != 1 || manifest.Items[0].Unknown || manifest.Items[0].Selection == nil || manifest.Items[0].Selection.FileID != "file2" {
		t.Fatal(manifest.Items)
	}
}
func TestStandaloneManualTargetContentRejectsWrongForeignAndDemotedReceipts(t *testing.T) {
	for _, failure := range []string{"hash", "foreign", "demoted"} {
		t.Run(failure, func(t *testing.T) {
			f, request, body := manualContentFixture(t)
			if failure == "hash" {
				body = coordinatorModJar(t, "9.0")
			}
			upload, err := f.s.config.Content.StageUpload(t.Context(), "alpha", "manual.jar", bytes.NewReader(body), f.actor)
			if err != nil {
				t.Fatal(err)
			}
			request.ContentManual = map[string]string{"example": upload.ID}
			if failure == "foreign" || failure == "demoted" {
				if _, err = f.s.config.DB.Exec("INSERT INTO users(id,username,role,created_at,updated_at)VALUES(2,'other','admin','now','now')"); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "foreign" {
				f.s.config.DB.Exec("UPDATE content_stages SET actor_id=2 WHERE id=?", upload.ID)
			}
			if failure == "demoted" {
				f.s.config.DB.Exec("UPDATE users SET role='operator' WHERE id=1")
			}
			job, err := f.s.Preview(t.Context(), "alpha", request, f.actor)
			if failure == "demoted" {
				if domain.AsError(err).Code != domain.CodeForbidden {
					t.Fatal("demoted manual acquisition admitted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if done := f.wait(t, job); done.State == domain.JobSucceeded {
				t.Fatal("unsafe manual receipt qualified")
			}
			actual, _ := os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
			if bytes.Equal(actual, body) {
				t.Fatal("failed acquisition changed live content")
			}
		})
	}
}

func TestManualContentAdminRevocationAtAtomicCommitRestoresPrebootBytes(t *testing.T) {
	f, request, body := manualContentFixture(t)
	upload, err := f.s.config.Content.StageUpload(t.Context(), "alpha", "manual.jar", bytes.NewReader(body), f.actor)
	if err != nil {
		t.Fatal(err)
	}
	request.ContentManual = map[string]string{"example": upload.ID}
	ready := f.previewRequest(t, request)
	if _, err = f.s.config.DB.Exec("INSERT INTO users(id,username,role,created_at,updated_at)VALUES(2,'other','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	f.sealHook = func() error { _, err := f.s.config.DB.Exec("UPDATE users SET role='operator' WHERE id=1"); return err }
	job, err := f.s.EnqueueApply(t.Context(), "alpha", ready.ID, true, f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if done := f.wait(t, job); done.State == domain.JobSucceeded {
		t.Fatal("demoted manual capability committed")
	}
	current, _ := f.s.config.Lifecycle.UpdateSnapshot(t.Context(), "alpha")
	if current.Plan.Selection.MinecraftVersion != "1.20.1" {
		t.Fatal("demotion changed selected target")
	}
	actual, _ := os.ReadFile(filepath.Join(f.dirs.Server, "mods/example.jar"))
	if bytes.Equal(actual, body) {
		t.Fatal("demoted content postimage survived preboot compensation")
	}
}
