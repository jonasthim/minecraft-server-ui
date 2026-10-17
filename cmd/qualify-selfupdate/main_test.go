package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

// These are acquisition-helper tests, never installed-service evidence.
func TestFixtureArchiveUsesShippingVerifierAndStager(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			binary := []byte("isolated acquisition-helper file bytes")
			archive, artifact, err := makeArchive(platform, binary, []byte("license"), []byte("notices"))
			if err != nil {
				t.Fatal(err)
			}
			peer := artifact
			peer.OS = "windows"
			peer.Name = "minecraft-ui-windows-amd64.zip"
			if platform == "windows" {
				peer.OS = "linux"
				peer.Name = "minecraft-ui-linux-amd64.tar.gz"
			}
			body, sig, err := signManifest(key, "v0.0.2", []selfupdate.Artifact{artifact, peer}, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			trust := selfupdate.Trust{Repository: fixtureRepository, PublicKeys: map[string]ed25519.PublicKey{fixtureKeyID: pub}}
			s := selfupdate.Stager{Dir: dir, Trust: trust, CurrentVersion: "v0.0.1", Schema: fixtureSchema(), OS: platform, Arch: "amd64"}
			stage, err := s.Stage(context.Background(), "v0.0.2", body, sig, bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			name := "minecraft-ui"
			if platform == "windows" {
				name += ".exe"
			}
			got, err := os.ReadFile(filepath.Join(dir, stage.ID, name))
			if err != nil || !bytes.Equal(got, binary) {
				t.Fatal("shipping stager did not preserve exact bytes", err)
			}
			if stage.Schema != fixtureSchema() || stage.Version != "v0.0.2" {
				t.Fatal("stage identity differs")
			}
			// New directory prevents idempotent-stage reuse hiding corrupt acquisition.
			s.Dir = t.TempDir()
			damaged := append([]byte{}, archive...)
			damaged[len(damaged)/2] ^= 1
			if _, err := s.Stage(context.Background(), "v0.0.2", body, sig, bytes.NewReader(damaged)); err == nil {
				t.Fatal("corrupted acquisition accepted")
			}
			body[0] ^= 1
			if _, err := selfupdate.VerifyManifest(trust, body, sig, "v0.0.2", "v0.0.1", fixtureSchema(), time.Now()); err == nil {
				t.Fatal("tampered signed metadata accepted")
			}
		})
	}
}

func TestPrepareWritesNoPrivateKeyAndRefusesExistingOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "fixtures")
	for _, name := range []string{"success", "negative", "peer-success", "peer-negative", "LICENSE", "NOTICES"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := prepareConfig{Success: filepath.Join(dir, "success"), Negative: filepath.Join(dir, "negative"), PeerSuccess: filepath.Join(dir, "peer-success"), PeerNegative: filepath.Join(dir, "peer-negative"), License: filepath.Join(dir, "LICENSE"), Notices: filepath.Join(dir, "NOTICES"), Output: out}
	if err := prepare(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"public-trust.json": true, "v0.0.2": true, "v0.0.3": true}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatal("unexpected fixture output")
	}
	for _, e := range entries {
		if !allowed[e.Name()] {
			t.Fatal("unexpected fixture file", e.Name())
		}
	}
	trustBody, err := os.ReadFile(filepath.Join(out, "public-trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	var trust map[string]json.RawMessage
	if err := json.Unmarshal(trustBody, &trust); err != nil || len(trust) != 3 {
		t.Fatal("public trust has unexpected fields", err)
	}
	var keys map[string]string
	if err := json.Unmarshal(trust["public_keys"], &keys); err != nil || len(keys) != 1 {
		t.Fatal("unexpected public keys", err)
	}
	pub, err := base64.StdEncoding.Strict().DecodeString(keys[fixtureKeyID])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatal("public fixture key is not exactly an Ed25519 public key", err)
	}
	for _, version := range []string{"v0.0.2", "v0.0.3"} {
		files, err := os.ReadDir(filepath.Join(out, version))
		if err != nil || len(files) != 4 {
			t.Fatal("unexpected release files", err)
		}
		allowedFiles := map[string]bool{"release.json": true, "release.sig": true, "minecraft-ui-linux-amd64.tar.gz": true, "minecraft-ui-windows-amd64.zip": true}
		for _, file := range files {
			if file.IsDir() || !allowedFiles[file.Name()] {
				t.Fatal("unexpected nested fixture material", file.Name())
			}
		}
	}
	if err := prepare(context.Background(), cfg); err == nil {
		t.Fatal("preexisting output accepted")
	}
}
