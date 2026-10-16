package modpack

import (
	"bytes"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func restrictedCFPack(t *testing.T) (*packFixture, catalog.Selection, []byte) {
	t.Helper()
	f := newPackFixture(t)
	f.c.projectID = "123"
	b := packZip(t, map[string][]byte{"config/a.cfg": []byte("server config"), "start.sh": []byte("never execute"), "server.properties": []byte("never replace"), "world/level.dat": []byte("never import")})
	file := catalog.File{ID: "200", Filename: "official-serverpack.zip", Size: int64(len(b)), Hashes: map[string]string{"sha256": digest(b)}, OfficialURL: "https://www.curseforge.com/minecraft/modpacks/example/files/200"}
	original := catalog.Selection{Provider: catalog.CurseForge, ProjectID: "123", VersionID: "100", FileID: "100"}
	f.c.versions = map[string]catalog.Version{"100": {ID: "100", ProjectID: "123", Name: "Original pack", ServerPackFileID: "200", Files: []catalog.File{{ID: "100"}}, OfficialURL: "https://www.curseforge.com/minecraft/modpacks/example/files/100"}, "200": {ID: "200", ProjectID: "123", Name: "Official server pack", IsServerPack: true, Files: []catalog.File{file}, Compatibility: catalog.Compatibility{Minecraft: []string{"1.20.1"}, Loaders: []string{"fabric"}, ServerSide: "supported"}, OfficialURL: file.OfficialURL}}
	f.c.downloadError = domain.E(domain.CodeUpstreamError, "restricted publisher artifact")
	return f, original, b
}
func TestRestrictedCFOuterArchiveVerifiedResume(t *testing.T) {
	f, selection, b := restrictedCFPack(t)
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: selection}, packAdmin)
	if e != nil || p.ArchiveManual == nil || p.Source.SHA256 != "" || len(p.Changes) != 0 || len(p.Blockers) == 0 || p.Source.ArtifactSelection.VersionID != "200" {
		t.Fatal(p, e)
	}
	if _, e := f.s.StageArchive(t.Context(), "alpha", selection, bytes.NewReader(bytes.Repeat([]byte{0}, len(b))), packAdmin); e == nil {
		t.Fatal("wrong archive hash accepted")
	}
	receipt, e := f.s.StageArchive(t.Context(), "alpha", selection, bytes.NewReader(b), packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	p, e = f.s.Preview(t.Context(), "alpha", Request{Pack: selection, ArchiveReceiptID: receipt.ID, PreviousPreviewID: p.ID}, packAdmin)
	if e != nil || p.ArchiveManual != nil || len(p.Blockers) > 0 || len(p.Excluded) != 3 || p.Source.SHA256 != digest(b) {
		t.Fatal(p, e)
	}
	if j := f.apply(t, p); j.State != domain.JobSucceeded {
		t.Fatal(j)
	}
	config, e := os.ReadFile(filepath.Join(f.dirs.Server, "config/a.cfg"))
	if e != nil || string(config) != "server config" {
		t.Fatal(string(config), e)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "world/level.dat")); !os.IsNotExist(e) {
		t.Fatal("world imported", e)
	}
	if _, e := os.Stat(filepath.Join(f.dirs.Server, "start.sh")); !os.IsNotExist(e) {
		t.Fatal("host script imported", e)
	}
}
func TestArchiveReceiptRejectsForeignExpiredTamperedAndRemappedSource(t *testing.T) {
	for _, scenario := range []string{"foreign", "expired", "tampered", "remapped"} {
		t.Run(scenario, func(t *testing.T) {
			f, selection, b := restrictedCFPack(t)
			receipt, e := f.s.StageArchive(t.Context(), "alpha", selection, bytes.NewReader(b), packAdmin)
			if e != nil {
				t.Fatal(e)
			}
			actor := packAdmin
			switch scenario {
			case "foreign":
				actor = domain.User{ID: 2, Role: domain.RoleAdmin}
				f.database.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'other','admin','now','now')")
			case "expired":
				f.database.Exec("UPDATE modpack_archives SET created_at='2000-01-01T00:00:00Z' WHERE id=?", receipt.ID)
			case "tampered":
				os.WriteFile(filepath.Join(f.dirs.State, "modpack", receipt.ID+".blob"), bytes.Repeat([]byte{0}, len(b)), 0600)
			case "remapped":
				v := f.c.versions["100"]
				v.ServerPackFileID = "201"
				f.c.versions["100"] = v
				v = f.c.versions["200"]
				v.ID = "201"
				v.Files[0].ID = "201"
				f.c.versions["201"] = v
			}
			if _, e = f.s.Preview(t.Context(), "alpha", Request{Pack: selection, ArchiveReceiptID: receipt.ID}, actor); e == nil {
				t.Fatal("unsafe archive receipt reused")
			}
		})
	}
}

func TestArchiveManualApplyRejectsAdminDemotionAtCommit(t *testing.T) {
	f, selection, b := restrictedCFPack(t)
	receipt, e := f.s.StageArchive(t.Context(), "alpha", selection, bytes.NewReader(b), packAdmin)
	if e != nil {
		t.Fatal(e)
	}
	p, e := f.s.Preview(t.Context(), "alpha", Request{Pack: selection, ArchiveReceiptID: receipt.ID}, packAdmin)
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
		t.Fatal("admin-only archive applied after demotion", j)
	}
	if !demoted {
		t.Fatal("did not reach authority change seam")
	}
	var n int
	if e = f.database.QueryRow("SELECT count(*) FROM modpack_baselines WHERE instance_id='alpha'").Scan(&n); e != nil || n != 0 {
		t.Fatal("baseline committed", n, e)
	}
	if _, e = os.Stat(filepath.Join(f.dirs.Server, "config/a.cfg")); !os.IsNotExist(e) {
		t.Fatal("admin-only publication not rolled back", e)
	}
}
