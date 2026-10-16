package modpack

import (
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
