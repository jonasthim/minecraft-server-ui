package loaderutil

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestLoaderCatalogOrdersNumericBuilds(t *testing.T) {
	body := `<metadata><groupId>net.neoforged</groupId><artifactId>neoforge</artifactId><versioning><versions><version>20.4.9</version><version>20.4.10</version><version>26.1.2</version></versions></versioning></metadata>`
	p := NewInstallerProvider(t.TempDir(), &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}, domain.FlavorNeoForge)
	versions, e := p.Loaders(context.Background(), "1.20.4")
	if e != nil || len(versions) != 2 || versions[0] != "20.4.10" {
		t.Fatal(versions, e)
	}
	games, e := p.Versions(context.Background())
	if e != nil || len(games) != 2 || games[0] != "26.1" {
		t.Fatal(games, e)
	}
}
func TestUnavailableTupleAndMalformedCatalogFailBeforeMutation(t *testing.T) {
	for _, body := range []string{`<metadata><groupId>net.minecraftforge</groupId><artifactId>forge</artifactId><versioning><versions><version>1.20.1-47.4.0</version></versions></versioning></metadata>`, `<broken`, `<metadata><groupId>untrusted</groupId><artifactId>forge</artifactId><versioning><versions><version>1.20.1-47.4.0</version></versions></versioning></metadata>`} {
		p := NewInstallerProvider(t.TempDir(), &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			if !strings.HasSuffix(r.URL.Path, "maven-metadata.xml") {
				t.Fatal("requested artifact for unavailable tuple")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}, domain.FlavorForge)
		if _, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.999"}); e == nil {
			t.Fatal("unavailable/malformed tuple accepted")
		}
	}
}
