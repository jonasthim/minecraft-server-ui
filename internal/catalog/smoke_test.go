package catalog

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"os"
	"testing"
	"time"
)

// Explicit opt-in read-only publisher/CDN verification; default suite stays network-free.
func TestOfficialSmoke(t *testing.T) {
	if os.Getenv("CATALOG_OFFICIAL_SMOKE") != "1" {
		t.Skip("set CATALOG_OFFICIAL_SMOKE=1 for official read-only metadata/CDN smoke")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d, e := db.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	s, e := New(Config{DB: d, ArtifactDir: t.TempDir(), UserAgent: "minecraft-server-ui/0.0.0-dev (+https://github.com/jonasthim/minecraft-server-ui)"})
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range []Query{{Provider: Modrinth, Kind: Mod, Text: "fabric api", Loader: "fabric", Minecraft: "1.21.1", Limit: 2}, {Provider: Modrinth, Kind: Plugin, Text: "viaversion", Loader: "paper", Limit: 2}, {Provider: Modrinth, Kind: Modpack, Text: "optimized", Limit: 2}, {Provider: Hangar, Kind: Plugin, Text: "ViaVersion", Loader: "paper", Limit: 2}} {
		r, e := s.Search(ctx, q)
		if e != nil || len(r.Projects) == 0 {
			t.Fatalf("%s %s %+v %v", q.Provider, q.Kind, r, e)
		}
		p := r.Projects[0]
		t.Logf("%s %s search: id=%s total=%d", q.Provider, q.Kind, p.ID, r.Total)
		v, e := s.Versions(ctx, p.Source, p.ID, Query{Kind: p.Kind, Loader: q.Loader, Minecraft: q.Minecraft, Limit: 2})
		if e != nil || len(v.Versions) == 0 {
			t.Fatalf("versions %+v %v", v, e)
		}
		t.Logf("%s exact version=%s upstream_id=%s files=%d environment=%s", p.Source, v.Versions[0].ID, v.Versions[0].UpstreamID, len(v.Versions[0].Files), v.Versions[0].Compatibility.Environment)
	}
	versions, e := s.Versions(ctx, Modrinth, "P7dR8mSH", Query{Minecraft: "1.21.1", Loader: "fabric", Limit: 1})
	if e != nil || len(versions.Versions) == 0 {
		t.Fatal(e)
	}
	v := versions.Versions[0]
	a, e := s.Download(ctx, Selection{Provider: Modrinth, ProjectID: v.ProjectID, VersionID: v.ID, FileID: v.Files[0].ID})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("verified inert FabricAPI artifact: %d bytes sha256=%s", a.File.Size, a.SHA256)
}
