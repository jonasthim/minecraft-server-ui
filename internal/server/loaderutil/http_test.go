package loaderutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestDownloadVerifiesPublishedDigest(t *testing.T) {
	b := []byte("publisher artifact")
	h := sha256.Sum256(b)
	c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b))), Header: make(http.Header)}, nil
	})})
	a := domain.Artifact{SourceURL: "https://maven.fabricmc.net/net/fabricmc/fabric-loader/0.16.14/fabric-loader-0.16.14.jar", Size: int64(len(b)), Checksum: domain.Checksum{Algorithm: "sha256", Value: hex.EncodeToString(h[:])}}
	if _, err := c.Download(context.Background(), a, io.Discard); err != nil {
		t.Fatal(err)
	}
	a.Checksum.Value = strings.Repeat("0", 64)
	if _, err := c.Download(context.Background(), a, io.Discard); err == nil {
		t.Fatal("bad published digest accepted")
	}
}
func TestUnsafeURLAndPaths(t *testing.T) {
	for _, u := range []string{"http://maven.fabricmc.net/a.jar", "https://evil.invalid/a.jar", "https://maven.fabricmc.net/a.jar?token=secret", "https://user:pass@maven.fabricmc.net/a.jar", "https://maven.fabricmc.net/%2e%2e/a.jar"} {
		if ValidURL(u) == nil {
			t.Errorf("accepted %q", u)
		}
	}
	for _, p := range []string{"../escape", "a/../b", "/absolute", "C:/drive", "a//b", "a/CON", "a/b. "} {
		if SafePath(p) == nil {
			t.Errorf("accepted %q", p)
		}
	}
}
func TestCancellationBeforeRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(nil).Bytes(ctx, "https://maven.fabricmc.net/a", 128); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestMalformedMetadataAndSanitizedNetworkErrors(t *testing.T) {
	for _, body := range []string{"{", `{} {}`, `{"wrong":"value"} trailing`} {
		c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})
		var out map[string]any
		if _, e := c.JSON(context.Background(), "https://meta.fabricmc.net/v2/versions/game", &out); e == nil {
			t.Fatal("malformed JSON accepted", body)
		}
	}
	c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) { return nil, fmt.Errorf("signed secret token=abc") })})
	if _, e := c.Bytes(context.Background(), "https://meta.fabricmc.net/v2/versions/game", 128); e == nil || strings.Contains(e.Error(), "abc") {
		t.Fatal(e)
	}
}
func TestChecksumFallbackOnlyOnAbsentStrongerHash(t *testing.T) {
	for _, status := range []int{403, 404, 500} {
		c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
			body := strings.Repeat("a", 40)
			code := 200
			if strings.HasSuffix(r.URL.Path, ".sha256") {
				code = status
			}
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})
		sum, _, e := c.Checksum(context.Background(), "https://maven.fabricmc.net/a.jar")
		if status == 404 {
			if e != nil || sum.Algorithm != "sha1" {
				t.Fatal(sum, e)
			}
		} else if e == nil {
			t.Fatal("authorization/failure downgraded checksum")
		}
	}
}
func TestMetadataLimitAndRedirectRefusal(t *testing.T) {
	c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://evil.invalid/secret"}}}, nil
	})})
	if _, e := c.Bytes(context.Background(), "https://meta.fabricmc.net/v2/versions/game", 128); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal(e)
	}
	c = NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 129))), Header: make(http.Header)}, nil
	})})
	if _, e := c.Bytes(context.Background(), "https://meta.fabricmc.net/v2/versions/game", 128); e == nil {
		t.Fatal("oversized metadata accepted")
	}
}
