package selfupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

var fixtureNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func signedFixture(t *testing.T) (Trust, Manifest, []byte, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("binary"))
	digest := hex.EncodeToString(h[:])
	m := Manifest{Format: 1, Repository: "example/manager", Version: "v1.2.3", PublishedAt: fixtureNow.Add(-time.Hour), ExpiresAt: fixtureNow.Add(time.Hour), SchemaBefore: 16, SchemaAfter: 16, Artifacts: []Artifact{{OS: "linux", Arch: "amd64", Name: "minecraft-ui-linux-amd64.tar.gz", Size: 1024, SHA256: digest, BinarySize: 6, BinarySHA256: digest}, {OS: "windows", Arch: "amd64", Name: "minecraft-ui-windows-amd64.zip", Size: 1024, SHA256: digest, BinarySize: 6, BinarySHA256: digest}}}
	b, _ := json.Marshal(m)
	sig, _ := json.Marshal(Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))})
	return Trust{Repository: m.Repository, PublicKeys: map[string]ed25519.PublicKey{"fixture": pub}}, m, b, sig
}
func TestVerifyManifestTrustAndBinding(t *testing.T) {
	trust, m, b, sig := signedFixture(t)
	got, err := VerifyManifest(trust, b, sig, m.Version, "v1.2.2", 16, fixtureNow)
	if err != nil || got.Version != m.Version {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	tests := []struct {
		name         string
		trust        Trust
		body, sig    []byte
		tag, current string
		schema       int64
		now          time.Time
	}{
		{"missing trust", Trust{}, b, sig, m.Version, "v1.2.2", 16, fixtureNow},
		{"changed signed bytes", trust, append(append([]byte{}, b...), byte(' ')), sig, m.Version, "v1.2.2", 16, fixtureNow},
		{"wrong tag", trust, b, sig, "v1.2.4", "v1.2.2", 16, fixtureNow},
		{"replay", trust, b, sig, m.Version, m.Version, 16, fixtureNow},
		{"downgrade", trust, b, sig, m.Version, "v2.0.0", 16, fixtureNow},
		{"expired", trust, b, sig, m.Version, "v1.2.2", 16, fixtureNow.Add(2 * time.Hour)},
		{"future", trust, b, sig, m.Version, "v1.2.2", 16, fixtureNow.Add(-2 * time.Hour)},
		{"schema change", trust, b, sig, m.Version, "v1.2.2", 17, fixtureNow},
		{"invalid current", trust, b, sig, m.Version, "latest", 16, fixtureNow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := VerifyManifest(tt.trust, tt.body, tt.sig, tt.tag, tt.current, tt.schema, tt.now); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}
func TestStableVersionStrictness(t *testing.T) {
	for _, v := range []string{"v1.2.3", "v26.3.0", "v0.1.0"} {
		if _, err := ParseStable(v); err != nil {
			t.Fatal(v, err)
		}
	}
	for _, v := range []string{"1.2.3", "v01.2.3", "v1.2.3-beta", "v1.2.3+meta", "v1.2", "v1.2.3/../x", "v18446744073709551616.0.0", "0.0.0-dev"} {
		if _, err := ParseStable(v); err == nil {
			t.Fatal("accepted", v)
		}
	}
}
