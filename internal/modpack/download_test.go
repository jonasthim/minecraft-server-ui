package modpack

import (
	"bytes"
	"io"
	"net/http"
	"net/netip"
	"testing"
)

func TestPackTransportRejectsCredentialsPrivateAddressesAndNonSpecHosts(t *testing.T) {
	for _, raw := range []string{"http://cdn.modrinth.com/a.jar", "https://cdn.modrinth.com:443/a.jar", "https://user:secret@github.com/a.jar", "https://127.0.0.1/a.jar", "https://cdn.modrinth.com.evil.invalid/a.jar", "https://github.com/a.jar?token=secret", "https://gitlab.com/%2e%2e/a.jar"} {
		if _, e := allowedPackURL(raw); e == nil {
			t.Fatal("unsafe pack URL allowed", raw)
		}
	}
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "::ffff:192.168.1.1", "2001:db8::1"} {
		if publicPackIP(netip.MustParseAddr(raw)) {
			t.Fatal("unsafe resolved address allowed", raw)
		}
	}
	for _, raw := range []string{"https://cdn.modrinth.com/data/a/versions/b/a.jar", "https://github.com/a/b/releases/download/v1/a.jar", "https://raw.githubusercontent.com/a/b/main/file.cfg", "https://gitlab.com/a/b/-/raw/main/file.cfg"} {
		if _, e := allowedPackURL(raw); e != nil {
			t.Fatal(raw, e)
		}
	}
}

type packRoundTripper func(*http.Request) (*http.Response, error)

func (fn packRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestDownloadedMembersUseSharedDestinationValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     []byte
		accepted bool
	}{
		{"config/a.cfg", []byte("valid UTF-8 text"), true},
		{"config/a.cfg", []byte{255, 1}, false},
		{"kubejs/server_scripts/a.js", []byte{0, 1}, false},
		{"kubejs/assets/example/icon.png.mcmeta", []byte{0, 1}, false},
		{"config/a.cfg", bytes.Repeat([]byte("x"), MaxConfigBytes+1), false},
		{"kubejs/assets/example/icon.png", []byte{0, 255, 1}, true},
		{"kubejs/data/example/structure.nbt", []byte{0, 255, 1}, true},
		{"config/icon.png", []byte{0, 255, 1}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPackFixture(t)
			client := &http.Client{Transport: packRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(test.body)), Body: io.NopCloser(bytes.NewReader(test.body))}, nil
			})}
			file := PackFile{Path: test.name, Size: int64(len(test.body)), Hashes: map[string]string{"sha256": digest(test.body)}, Downloads: []string{"https://cdn.modrinth.com/verified-index-member"}}
			st, e := f.s.downloadPackFileUsing(t.Context(), "alpha", packAdmin.ID, file, client)
			if (e == nil) != test.accepted {
				t.Fatal("download destination policy mismatch", st, e)
			}
		})
	}
}
