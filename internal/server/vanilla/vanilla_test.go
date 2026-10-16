package vanilla

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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
func fixture(t *testing.T, mutate func(map[string]any), manifestMutate func(map[string]any)) *Provider {
	t.Helper()
	meta := map[string]any{"id": "1.12.2", "type": "release", "javaVersion": map[string]any{"majorVersion": 8}, "downloads": map[string]any{"server": map[string]any{"sha1": strings.Repeat("a", 40), "size": 25, "url": "https://piston-data.mojang.com/v1/objects/" + strings.Repeat("a", 40) + "/server.jar"}}}
	if mutate != nil {
		mutate(meta)
	}
	data, _ := json.Marshal(meta)
	digest := sha1.Sum(data)
	h := hex.EncodeToString(digest[:])
	u := "https://piston-meta.mojang.com/v1/packages/" + h + "/1.12.2.json"
	manifest := map[string]any{"versions": []any{map[string]any{"id": "1.12.2", "type": "release", "url": u, "sha1": h}, map[string]any{"id": "1.12.1", "type": "release"}, map[string]any{"id": "26.3-rc-1", "type": "snapshot"}}}
	if manifestMutate != nil {
		manifestMutate(manifest)
	}
	index, _ := json.Marshal(manifest)
	return New(t.TempDir(), &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("User-Agent"), "https://github.com/jonasthim/minecraft-server-ui") {
			t.Fatal("missing identifying UA")
		}
		body := index
		if r.URL.String() == u {
			body = data
		} else if r.URL.String() != manifestURL {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})})
}
func TestResolvePublishedStableRelease(t *testing.T) {
	p := fixture(t, nil, nil)
	v, e := p.Versions(context.Background())
	if e != nil || fmt.Sprint(v) != "[1.12.2]" {
		t.Fatalf("versions %v %v", v, e)
	}
	plan, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"})
	if e != nil {
		t.Fatal(e)
	}
	if plan.Requirements.JavaMajor != 8 || len(plan.Artifacts) != 1 || plan.Artifacts[0].Checksum.Algorithm != "sha1" || plan.Artifacts[0].Provider != "mojang" {
		t.Fatalf("bad pinned plan %+v", plan)
	}
}
func TestRejectUnsupportedAndUnpublishedSelections(t *testing.T) {
	p := fixture(t, nil, nil)
	for _, s := range []domain.VersionSelection{{Flavor: domain.FlavorPaper, MinecraftVersion: "1.12.2"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.1"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2", LoaderVersion: "latest"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.3"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "26.3-rc-1"}} {
		if _, e := p.Resolve(context.Background(), s); e == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
}
func TestRejectUntrustedMetadata(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"wrong id":   func(m map[string]any) { m["id"] = "1.13" },
		"wrong type": func(m map[string]any) { m["type"] = "snapshot" },
		"wrong Java": func(m map[string]any) { m["javaVersion"] = map[string]any{"majorVersion": 21} },
		"unsafe artifact URL": func(m map[string]any) {
			m["downloads"].(map[string]any)["server"].(map[string]any)["url"] = "https://evil.invalid/server.jar?token=secret"
		},
		"bad size": func(m map[string]any) { m["downloads"].(map[string]any)["server"].(map[string]any)["size"] = -1 },
		"bad hash": func(m map[string]any) { m["downloads"].(map[string]any)["server"].(map[string]any)["sha1"] = "broken" },
	} {
		t.Run(name, func(t *testing.T) {
			p := fixture(t, change, nil)
			_, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"})
			if e == nil {
				t.Fatal("accepted malicious metadata")
			}
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("secret in error")
			}
		})
	}
}
func TestRejectManifestMetadataURL(t *testing.T) {
	p := fixture(t, nil, func(m map[string]any) {
		m["versions"].([]any)[0].(map[string]any)["url"] = "https://evil.invalid/metadata?secret=token"
	})
	if _, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"}); e == nil {
		t.Fatal("accepted external metadata")
	}
}

func TestMetadataChecksumMismatch(t *testing.T) {
	hash := strings.Repeat("a", 40)
	u := "https://piston-meta.mojang.com/v1/packages/" + hash + "/1.12.2.json"
	p := New(t.TempDir(), &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := `{"versions":[{"id":"1.12.2","type":"release","sha1":"` + hash + `","url":"` + u + `"}]}`
		if r.URL.String() == u {
			body = `{"id":"1.12.2","type":"release"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if _, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"}); e == nil || !strings.Contains(e.Error(), "metadata checksum mismatch") {
		t.Fatalf("expected metadata digest rejection, got %v", e)
	}
}
func TestInstallRejectsTamperedPlanBeforeRuntimeOrDownload(t *testing.T) {
	p := fixture(t, nil, nil)
	plan, e := p.Resolve(context.Background(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.2"})
	if e != nil {
		t.Fatal(e)
	}
	plan.Requirements.JavaMajor = 21
	if _, e = p.Install(context.Background(), "test", plan, domain.JavaRuntime{}, io.Discard); e == nil || !strings.Contains(e.Error(), "differs from official pinned") {
		t.Fatalf("tampered plan wasn't rejected: %v", e)
	}
}
