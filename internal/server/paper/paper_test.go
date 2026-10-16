package paper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixture(t *testing.T, change func(map[string]any)) *Provider {
	t.Helper()
	b := map[string]any{"id": 232, "channel": "STABLE", "downloads": map[string]any{"server:default": map[string]any{"name": "paper-1.21.4-232.jar", "size": 25, "checksums": map[string]any{"sha256": strings.Repeat("a", 64)}, "url": "https://fill-data.papermc.io/v1/objects/" + strings.Repeat("a", 64) + "/paper-1.21.4-232.jar"}}}
	if change != nil {
		change(b)
	}
	builds, _ := json.Marshal([]any{map[string]any{"id": 233, "channel": "ALPHA"}, b})
	project := `{"project":{"id":"paper","name":"Paper"},"versions":{"1.21":["1.21.4","1.21.4-pre1"],"1.11":["1.11.2"]}}`
	return New(t.TempDir(), &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("User-Agent"), "https://github.com/jonasthim/minecraft-server-ui") {
			t.Fatal("missing UA")
		}
		var body string
		switch r.URL.String() {
		case projectURL:
			body = project
		case projectURL + "/versions/1.21.4/builds":
			body = string(builds)
		default:
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
}
func TestCatalogAndExactStableBuild(t *testing.T) {
	p := fixture(t, nil)
	ctx := context.Background()
	v, e := p.Versions(ctx)
	if e != nil || fmt.Sprint(v) != "[1.21.4]" {
		t.Fatalf("versions %v %v", v, e)
	}
	b, e := p.Builds(ctx, "1.21.4")
	if e != nil || fmt.Sprint(b) != "[232]" {
		t.Fatalf("builds %v %v", b, e)
	}
	plan, e := p.Resolve(ctx, domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "232"})
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Artifacts) != 1 || plan.Artifacts[0].VersionID != "1.21.4/232" || plan.Artifacts[0].Checksum.Algorithm != "sha256" || plan.Requirements.JavaMajor != 21 {
		t.Fatalf("bad plan %+v", plan)
	}
}
func TestRejectFloatingUnpublishedExperimentalAndWrongFlavor(t *testing.T) {
	p := fixture(t, nil)
	for _, s := range []domain.VersionSelection{{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4"}, {Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "latest"}, {Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "0232"}, {Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "233"}, {Flavor: domain.FlavorPaper, MinecraftVersion: "1.12.2", LoaderVersion: "1"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.4", LoaderVersion: "232"}} {
		if _, e := p.Resolve(context.Background(), s); e == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
func TestRejectMalformedBuildArtifacts(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"url": func(m map[string]any) {
			m["downloads"].(map[string]any)["server:default"].(map[string]any)["url"] = "https://evil.invalid/server.jar?secret=token"
		},
		"hash": func(m map[string]any) {
			m["downloads"].(map[string]any)["server:default"].(map[string]any)["checksums"] = map[string]any{"sha256": "bad"}
		},
		"size": func(m map[string]any) { m["downloads"].(map[string]any)["server:default"].(map[string]any)["size"] = 0 },
		"filename": func(m map[string]any) {
			m["downloads"].(map[string]any)["server:default"].(map[string]any)["name"] = "../../server.jar"
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, change)
			if _, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "232"}); e == nil {
				t.Fatal("accepted malformed artifact")
			} else if strings.Contains(e.Error(), "secret") {
				t.Fatal("secret leak")
			}
		})
	}
}

func TestInstallRejectsTamperedPlanBeforeRuntimeOrDownload(t *testing.T) {
	p := fixture(t, nil)
	plan, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "232"})
	if e != nil {
		t.Fatal(e)
	}
	plan.Artifacts[0].SourceURL = "https://evil.invalid/secret"
	if _, e = p.Install(context.Background(), "test", plan, domain.JavaRuntime{}, io.Discard); e == nil || !strings.Contains(e.Error(), "differs from official pinned") {
		t.Fatalf("tampered plan wasn't rejected: %v", e)
	}
}
