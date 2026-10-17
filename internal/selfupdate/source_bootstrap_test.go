package selfupdate

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestBootstrapSourceExactPathsAndUpdateAllowlist(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	s, err := NewSource(trust)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "github.com" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatalf("unsafe request: %v", r.URL)
		}
		paths = append(paths, r.URL.Path)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("fixture")), Header: make(http.Header)}, nil
	})
	ctx := context.Background()
	tag := "v1.2.3"
	if _, _, err = s.BootstrapMetadata(ctx, tag); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"linux", "windows"} {
		r, e := s.BootstrapArchive(ctx, tag, p)
		if e != nil {
			t.Fatal(e)
		}
		r.Close()
	}
	want := []string{"bootstrap.json", "bootstrap.sig", "minecraft-ui-bootstrap-linux-amd64.tar.gz", "minecraft-ui-bootstrap-windows-amd64.zip"}
	if len(paths) != len(want) {
		t.Fatal(paths)
	}
	for i, name := range want {
		if paths[i] != "/"+trust.Repository+"/releases/download/"+tag+"/"+name {
			t.Fatal(paths[i])
		}
		if _, e := s.open(ctx, tag, name); e == nil {
			t.Fatal("bootstrap widened shipping update allowlist", name)
		}
	}
	for _, name := range []string{"release.json", "release.sig", archiveName("linux"), archiveName("windows"), "../bootstrap.json", "bootstrap.json?token=secret"} {
		if _, e := s.openBootstrap(ctx, tag, name); e == nil {
			t.Fatal("bootstrap source accepted", name)
		}
	}
	for _, p := range []string{"linux/amd64", "darwin", "../linux", "windows?token=secret"} {
		if _, e := s.BootstrapArchive(ctx, tag, p); e == nil {
			t.Fatal("invalid platform fetched")
		}
	}
	if _, _, e := s.BootstrapMetadata(ctx, "v1.2.3?secret"); e == nil {
		t.Fatal("invalid tag fetched")
	}
	if len(paths) != 4 {
		t.Fatal("rejected input performed network IO", paths)
	}
}

func TestBootstrapLatestTransportBindsExactVersionAnd404(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	s, e := NewSource(trust)
	if e != nil {
		t.Fatal(e)
	}
	for _, status := range []int{200, 404, 503} {
		s.discoveryClient.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://api.github.com/repos/"+trust.Repository+"/releases/latest" {
				t.Fatal(r.URL)
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"tag_name":"v9.8.7","draft":false,"prerelease":false,"published_at":"2026-10-16T12:00:00Z"}`)), Header: make(http.Header)}, nil
		})
		latest, e := s.Latest(context.Background())
		if status == 404 {
			if latest != nil || e != nil {
				t.Fatal(latest, e)
			}
			continue
		}
		if status == 503 {
			if e == nil {
				t.Fatal("network success fabricated")
			}
			continue
		}
		if e != nil || latest.Version != "v9.8.7" {
			t.Fatal(latest, e)
		}
		var paths []string
		s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			paths = append(paths, r.URL.Path)
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("fixture")), Header: make(http.Header)}, nil
		})
		if _, _, e = s.BootstrapMetadata(context.Background(), latest.Version); e != nil {
			t.Fatal(e)
		}
		r, e := s.BootstrapArchive(context.Background(), latest.Version, "linux")
		if e != nil {
			t.Fatal(e)
		}
		r.Close()
		for _, path := range paths {
			if !strings.Contains(path, "/releases/download/v9.8.7/") {
				t.Fatal("latest was not pinned", path)
			}
		}
	}
}

func TestBootstrapSourceBoundsAndSanitizesSignedURLErrors(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	s, e := NewSource(trust)
	if e != nil {
		t.Fatal(e)
	}
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (32<<10)+1))), Header: make(http.Header)}, nil
	})
	if _, _, e = s.BootstrapMetadata(context.Background(), "v1.2.3"); e == nil {
		t.Fatal("oversized metadata accepted")
	}
	s.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: "https://release-assets.githubusercontent.com/a?token=SECRET", Err: errors.New("SECRET")}
	})
	if _, _, e = s.BootstrapMetadata(context.Background(), "v1.2.3"); e == nil || strings.Contains(e.Error(), "SECRET") {
		t.Fatal("signed URL leaked", e)
	}
	if _, e = s.BootstrapArchive(context.Background(), "v1.2.3", "linux"); e == nil || strings.Contains(e.Error(), "SECRET") {
		t.Fatal("signed URL leaked", e)
	}
}
