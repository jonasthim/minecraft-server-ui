package selfupdate

import (
	"context"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSourceRedirectsAndAddresses(t *testing.T) {
	initial := "https://github.com/example/manager/releases/download/v1.2.3/release.json"
	for _, u := range []string{initial, "https://release-assets.githubusercontent.com/github-production-release-asset/123?signature=private"} {
		if err := releaseURL(u, initial, false); err != nil {
			t.Fatal(u, err)
		}
	}
	for _, u := range []string{"http://github.com/example/manager/releases/download/v1.2.3/release.json", "https://github.com/other/repo/releases/download/v1.2.3/release.json", "https://release-assets.githubusercontent.com:444/a", "https://github.com@127.0.0.1/x", "https://evil.example/a", "https://release-assets.githubusercontent.com/a#secret"} {
		if err := releaseURL(u, initial, false); err == nil {
			t.Fatal("allowed", u)
		}
	}
	if err := releaseURL(initial, initial, true); err == nil {
		t.Fatal("allowed redirect back from asset host")
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.2", "0.0.0.0", "100.64.1.1", "192.0.2.1", "198.18.0.1", "224.0.0.1", "::1", "::ffff:127.0.0.1", "2001:db8::1", "fe80::1", "fc00::1"} {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Fatal("unsafe dial address", ip)
		}
	}
	for _, ip := range []string{"140.82.114.3", "185.199.108.133", "2606:50c0:8000::154"} {
		if !publicAddress(netip.MustParseAddr(ip)) {
			t.Fatal("public rejected", ip)
		}
	}
}
func TestSourceBoundedAndUnconfigured(t *testing.T) {
	trust, _, _, _ := signedFixture(t)
	source, err := NewSource(trust)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	source.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "" || r.URL.Host != "github.com" {
			t.Fatal("unexpected source")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", MaxManifestBytes+1))), Header: make(http.Header)}, nil
	})
	if _, err := source.read(context.Background(), "v1.2.3", "release.json", MaxManifestBytes); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := source.read(context.Background(), "../evil", "release.json", 10); err == nil {
		t.Fatal("invalid tag fetched")
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err := NewSource(Trust{}); err == nil {
		t.Fatal("empty trust enabled source")
	}
}
