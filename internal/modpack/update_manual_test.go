package modpack

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

func targetManualFixture(t *testing.T) (*packFixture, string, privatePreview) {
	t.Helper()
	f := newPackFixture(t)
	entry := indexEntry("config/a.cfg", "required")
	entry["downloads"] = []string{"https://unapproved.invalid/a.cfg"}
	index := bytes.ReplaceAll(mrIndex(entry), []byte("1.20.1"), []byte("1.20.2"))
	body := packZip(t, map[string][]byte{"modrinth.index.json": index})
	archive := filepath.Join(t.TempDir(), "target.mrpack")
	os.WriteFile(archive, body, 0600)
	f.setPack(t, "two", map[string][]byte{})
	f.c.artifact.Path = archive
	f.c.artifact.SHA256 = digest(body)
	f.c.artifact.File.Size = int64(len(body))
	f.c.version.Files[0] = f.c.artifact.File
	f.c.version.Compatibility.Minecraft = []string{"1.20.2"}
	manifest, err := f.s.content.Snapshot(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	target := content.Tuple{Minecraft: "1.20.2", Loader: "fabric", LoaderVersion: "0.16.0"}
	prepared, err := f.s.previewForTarget(t.Context(), "alpha", Request{Pack: f.c.artifact.Selection}, packAdmin, manifest, manifest.Items, target, false, func(items []content.Installed) ([]content.Installed, []string, error) {
		return content.PrepareTargetCandidate(lifecycle.TargetProof{Selection: domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.2", LoaderVersion: "0.16.0"}, Runtime: domain.JavaRuntime{Major: 17}}, items)
	})
	if err != nil || len(prepared.Preview.Manual) != 1 {
		t.Fatal(prepared.Preview, err)
	}
	id := strings.Repeat("a", 32)
	generation := strings.Repeat("b", 32)
	if _, err = f.database.Exec("INSERT INTO lifecycle_installations(instance_id,generation,installation_json)VALUES('alpha',?,'{}')", generation); err != nil {
		t.Fatal(err)
	}
	var revision string
	f.database.QueryRow("SELECT updated_at FROM instances WHERE id='alpha'").Scan(&revision)
	staging, _ := json.Marshal(prepared)
	raw, _ := json.Marshal(map[string]any{"version": 1, "actor_id": 1, "pack_staging": json.RawMessage(staging), "plan": map[string]any{"id": id, "instance_id": "alpha", "target": domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.2", LoaderVersion: "0.16.0"}, "snapshot": map[string]string{"instance_revision": revision, "generation": generation, "content_revision": manifest.Revision, "pack_revision": "none"}}})
	if _, err = f.database.Exec("INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at)VALUES(?,'alpha',1,?,'now',?)", id, string(raw), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	return f, id, prepared
}
func TestTargetManualReceiptUsesNewTupleAndExactPublisherHashes(t *testing.T) {
	f, id, prepared := targetManualFixture(t)
	if _, err := f.s.StageUpdateMember(t.Context(), "alpha", id, "config/a.cfg", strings.NewReader("bad"), packAdmin); err == nil {
		t.Fatal("wrong target member hash accepted")
	}
	receipt, err := f.s.StageUpdateMember(t.Context(), "alpha", id, "config/a.cfg", strings.NewReader("jar"), packAdmin)
	if err != nil || receipt.Path != "config/a.cfg" || receipt.SHA256 != digest([]byte("jar")) {
		t.Fatal(receipt, err)
	}
	// Crash/ordinary-discard cleanup between acquisition and reprepare must not
	// remove the receipt now durably attached to this update preview.
	if err = f.s.cleanupStages(t.Context(), "alpha", false); err != nil {
		t.Fatal(err)
	}
	var acquired string
	if err = f.database.QueryRow("SELECT private_json FROM update_previews WHERE id=?", id).Scan(&acquired); err != nil || !strings.Contains(acquired, receipt.ReceiptID) {
		t.Fatal("manual acquisition not durably referenced", err)
	}
	request := prepared.Request
	request.Manual = map[string]string{receipt.Path: receipt.ReceiptID}
	next, err := f.s.previewForTarget(t.Context(), "alpha", request, packAdmin, prepared.Manifest, prepared.Manifest.Items, prepared.Preview.Tuple, false, func(items []content.Installed) ([]content.Installed, []string, error) { return items, nil, nil })
	if err != nil || len(next.Preview.Manual) > 0 || len(next.Preview.Blockers) > 0 {
		t.Fatal(next.Preview, err)
	}
	var count int
	f.database.QueryRow("SELECT count(*) FROM modpack_previews").Scan(&count)
	if count != 0 {
		t.Fatal("target preview entered ordinary apply table")
	}
	if _, err = os.Stat(filepath.Join(f.dirs.Server, "config/a.cfg")); !os.IsNotExist(err) {
		t.Fatal("manual stage changed live content")
	}
}

type updateDemotingReader struct {
	reader io.Reader
	demote func()
}

func (r *updateDemotingReader) Read(b []byte) (int, error) {
	if r.demote != nil {
		r.demote()
		r.demote = nil
	}
	return r.reader.Read(b)
}
func TestTargetManualReceiptRechecksAdminAtStagePublication(t *testing.T) {
	f, id, _ := targetManualFixture(t)
	if _, err := f.database.Exec("INSERT INTO users(id,username,role,created_at,updated_at)VALUES(2,'other','admin','now','now')"); err != nil {
		t.Fatal(err)
	}
	reader := &updateDemotingReader{reader: strings.NewReader("jar"), demote: func() {
		if _, err := f.database.Exec("UPDATE users SET role='operator' WHERE id=1"); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := f.s.StageUpdateMember(t.Context(), "alpha", id, "config/a.cfg", reader, packAdmin); err == nil {
		t.Fatal("revoked admin published target stage")
	}
	var count int
	f.database.QueryRow("SELECT count(*) FROM modpack_stages WHERE sha256!=''").Scan(&count)
	if count != 0 {
		t.Fatal("revoked body acquired published receipt")
	}
}
func TestTargetManualReceiptRejectsForeignExpiredAndChangedMetadata(t *testing.T) {
	for _, failure := range []string{"foreign", "expired", "metadata", "path"} {
		t.Run(failure, func(t *testing.T) {
			f, id, _ := targetManualFixture(t)
			actor := packAdmin
			member := "config/a.cfg"
			switch failure {
			case "foreign":
				f.database.Exec("INSERT INTO users(id,username,role,created_at,updated_at)VALUES(2,'other','admin','now','now')")
				actor.ID = 2
			case "expired":
				f.database.Exec("UPDATE update_previews SET expires_at='2000-01-01T00:00:00Z'")
			case "metadata":
				f.c.version.Name = "changed publisher proof"
			case "path":
				member = "world/level.dat"
			}
			if _, err := f.s.StageUpdateMember(t.Context(), "alpha", id, member, strings.NewReader("jar"), actor); err == nil {
				t.Fatal("unsafe manual receipt admitted")
			}
		})
	}
}
