//go:build linux

package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestStagingInsufficientSpace runs inside the bounded private tmpfs fixture,
// never by filling a host disk. The ordinary package run records an explicit skip.
func TestStagingInsufficientSpace(t *testing.T) {
	dir := os.Getenv("MINECRAFT_UI_TEST_STAGE_DISKFULL")
	if dir == "" {
		t.Skip("run testdata/insufficient_space.sh for isolated real ENOSPC")
	}
	binary := make([]byte, 2<<20)
	if _, err := rand.Read(binary); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"} {
		body := []byte("notice")
		if name == "minecraft-ui" {
			body = binary
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	trust, m, _, _ := signedFixture(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	trust.PublicKeys["fixture"] = pub
	m.Artifacts[0].Size = int64(archive.Len())
	m.Artifacts[0].SHA256 = digest(archive.Bytes())
	m.Artifacts[0].BinarySize = int64(len(binary))
	m.Artifacts[0].BinarySHA256 = digest(binary)
	body, _ := json.Marshal(m)
	sig, _ := json.Marshal(Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, body))})
	stager := Stager{Dir: dir, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
	if _, err := stager.Stage(context.Background(), m.Version, body, sig, bytes.NewReader(archive.Bytes())); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected real ENOSPC, got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed stage retained partial publication", entries, err)
	}
}
