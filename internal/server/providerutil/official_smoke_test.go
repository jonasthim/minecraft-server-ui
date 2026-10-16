package providerutil_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/server/paper"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/vanilla"
)

// Opt in explicitly: ordinary package tests are deterministic and network-free.
// This smoke resolves and downloads only. It never installs Java, starts a JVM,
// runs Minecraft, writes an EULA file or claims gameplay qualification.
func TestOfficialArtifactSmoke(t *testing.T) {
	if os.Getenv("MINECRAFT_UI_SERVER_SMOKE") != "1" {
		t.Skip("set MINECRAFT_UI_SERVER_SMOKE=1 for official metadata/artifact download")
	}
	for _, tc := range []struct {
		name      string
		provider  domain.DistributionProvider
		selection domain.VersionSelection
		publisher providerutil.Publisher
	}{
		{"vanilla-1.12.2", vanilla.New(t.TempDir(), nil), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"}, providerutil.Mojang},
		{"paper-1.21.4-build232", paper.New(t.TempDir(), nil), domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "232"}, providerutil.Paper},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			plan, e := tc.provider.Resolve(ctx, tc.selection)
			if e != nil {
				t.Fatal(e)
			}
			if len(plan.Artifacts) != 1 {
				t.Fatal("expected one exact artifact")
			}
			a := plan.Artifacts[0]
			sum, e := providerutil.NewClient(nil, tc.publisher).Download(ctx, a, io.Discard)
			if e != nil {
				t.Fatal(e)
			}
			t.Logf("selection=%+v java=%d url=%s size=%d published=%s:%s local_sha256=%s retrieved_at=%s", plan.Selection, plan.Requirements.JavaMajor, a.SourceURL, a.Size, a.Checksum.Algorithm, a.Checksum.Value, sum, a.RetrievedAt.Format(time.RFC3339))
		})
	}
}
