package loaderutil_test

import (
	"context"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server/fabric"
	"github.com/jonasthim/minecraft-server-ui/internal/server/forge"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/neoforge"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialLoaderInstallOnly(t *testing.T) {
	if os.Getenv("MINECRAFT_UI_LOADER_SMOKE") != "1" {
		t.Skip("explicit official installer-only opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	dir := t.TempDir()
	cases := []struct {
		name      string
		provider  domain.DistributionProvider
		selection domain.VersionSelection
	}{{"fabric-legacy", fabric.New(dir, nil), domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.17.1", LoaderVersion: "0.16.14"}}, {"fabric-bundled", fabric.New(dir, nil), domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.14"}}, {"forge-legacy", forge.New(dir, nil), domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.12.2", LoaderVersion: "14.23.5.2860"}}, {"forge-modern", forge.New(dir, nil), domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.0"}}, {"neoforge-modern", neoforge.New(dir, nil), domain.VersionSelection{Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.20.4", LoaderVersion: "20.4.253"}}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if filter := os.Getenv("MINECRAFT_UI_LOADER_TUPLE"); filter != "" && filter != c.name {
				t.Skip("tuple filter")
			}
			plan, e := c.provider.Resolve(ctx, c.selection)
			if e != nil {
				t.Fatal(e)
			}
			runtime, e := managedruntime.New(dir, nil).EnsureRequirements(ctx, plan.Requirements, nil)
			if e != nil {
				t.Fatal(e)
			}
			server := filepath.Join(dir, "instances", c.name, "server")
			if e = os.MkdirAll(filepath.Join(server, "world"), 0700); e != nil {
				t.Fatal(e)
			}
			for _, n := range []string{"world/level.dat", "server.properties", "eula.txt"} {
				if e = os.WriteFile(filepath.Join(server, n), []byte("unchanged; no EULA consent"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			spec, e := c.provider.Install(ctx, c.name, plan, runtime, nil)
			if e != nil {
				t.Fatal(e)
			}
			for _, n := range []string{"world/level.dat", "server.properties", "eula.txt"} {
				b, e := os.ReadFile(filepath.Join(server, n))
				if e != nil || string(b) != "unchanged; no EULA consent" {
					t.Fatal("active file changed", n, e)
				}
			}
			if e = loaderutil.VerifyLaunch(ctx, dir, c.name, spec); e != nil {
				t.Fatal("offline verification", e)
			}
			for _, a := range plan.Artifacts {
				t.Logf("artifact=%s bytes=%d %s=%s", a.ID, a.Size, a.Checksum.Algorithm, a.Checksum.Value)
			}
			b, _ := json.Marshal(spec)
			t.Logf("runtime=%s argv_count=%d cwd=%s launch_sha256=%s", runtime.ID, len(spec.Args), spec.WorkingDir, loaderutil.Digest("sha256", b))
			again, e := c.provider.Install(ctx, c.name, plan, runtime, nil)
			if e != nil {
				t.Fatal("reuse", e)
			}
			b2, _ := json.Marshal(again)
			if string(b) != string(b2) {
				t.Fatal("reuse changed launch proposal")
			}
		})
	}
}
