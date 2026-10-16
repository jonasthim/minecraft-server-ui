package fabric

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRejectFloorBeforeNetwork(t *testing.T) {
	p := New(t.TempDir(), nil)
	if _, err := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.13.2", LoaderVersion: "1.0.0"}); err == nil {
		t.Fatal("floor accepted")
	}
}
func TestRejectFloatingLoader(t *testing.T) {
	p := New(t.TempDir(), nil)
	if _, err := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.2", LoaderVersion: "latest"}); err == nil {
		t.Fatal("floating loader accepted")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCatalogAllowsHistoricalMainClassShape(t *testing.T) {
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		body := `[{"version":"1.20.1","stable":true}]`
		if strings.Contains(r.URL.Path, "loader/") {
			body = `[{"loader":{"version":"0.2.0.71","maven":"net.fabricmc:fabric-loader:0.2.0.71"},"intermediary":{"version":"1.20.1","maven":"net.fabricmc:intermediary:1.20.1"},"launcherMeta":{"mainClass":"old.client.Main"}}]`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	versions, e := New(t.TempDir(), c).Loaders(context.Background(), "1.20.1")
	if e != nil || len(versions) != 1 {
		t.Fatal(versions, e)
	}
}
