package modpack

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in only: official metadata/archive inspection, no game execution.
func TestOfficialPackInspection(t *testing.T) {
	if os.Getenv("MODPACK_OFFICIAL_SMOKE") != "1" {
		t.Skip("set MODPACK_OFFICIAL_SMOKE=1 for inert official pack inspection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, e := db.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	service, e := catalog.New(catalog.Config{DB: database, ArtifactDir: t.TempDir(), UserAgent: "minecraft-server-ui/0.0.0-dev (+https://github.com/jonasthim/minecraft-server-ui)"})
	if e != nil {
		t.Fatal(e)
	}
	project, e := service.Project(ctx, catalog.Modrinth, "adrenaline")
	if e != nil {
		t.Fatal(e)
	}
	versions, e := service.Versions(ctx, catalog.Modrinth, project.ID, catalog.Query{Minecraft: "1.20.1", Loader: "fabric", Limit: 10})
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range versions.Versions {
		if v.Compatibility.ServerSide != "supported" {
			continue
		}
		selection := catalog.Selection{Provider: catalog.Modrinth, ProjectID: project.ID, VersionID: v.ID, FileID: v.Files[0].ID}
		artifact, e := service.Download(ctx, selection)
		if e != nil {
			t.Fatal(e)
		}
		file, e := os.Open(artifact.Path)
		if e != nil {
			t.Fatal(e)
		}
		defer file.Close()
		pack, e := ParseArchive(ctx, file, artifact.File.Size, "modrinth")
		if e != nil {
			t.Fatalf("official server-declared pack project=%s version=%s sha256=%s: %v", project.ID, v.ID, artifact.SHA256, e)
		}
		t.Logf("official server-declared inert pack project=%s version=%s bytes=%d sha256=%s server-members=%d excluded=%d tuple=%s/%s/%s blockers=%v", project.ID, v.ID, artifact.File.Size, artifact.SHA256, len(pack.Files), len(pack.Excluded), pack.Minecraft, pack.Loader, pack.LoaderVersion, pack.Blockers)
		fixture := newPackFixture(t)
		names := []string{}
		for name, member := range pack.Files {
			if strings.HasPrefix(name, "mods/") && member.ArchiveEntry == "" && member.Server == "required" && member.Size <= 16<<20 {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) == 0 {
			t.Fatal("official pack has no bounded required server JAR")
		}
		member := pack.Files[names[0]]
		st, e := fixture.s.downloadPackFile(ctx, "alpha", packAdmin.ID, member)
		if e != nil {
			t.Fatal(e)
		}
		jar, e := fixture.s.openStage(ctx, "alpha", st)
		if e != nil {
			t.Fatal(e)
		}
		descriptor, e := content.InspectPackReader(ctx, jar, st.Size)
		jar.Close()
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("official digest-verified inert member path=%s bytes=%d sha256=%s descriptor=%s version=%s unknown=%v", member.Path, st.Size, st.SHA256, descriptor.ID, descriptor.Version, descriptor.Unknown)
		return
	}
	t.Fatal("no server-supported exact release returned")
}
