package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func archiveFixture(t *testing.T, platform string, names []string) []byte {
	t.Helper()
	var b bytes.Buffer
	if platform == "linux" {
		gz := gzip.NewWriter(&b)
		w := tar.NewWriter(gz)
		for _, name := range names {
			data := []byte("notice")
			if name == "minecraft-ui" {
				data = []byte("binary")
			}
			if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0755, Typeflag: tar.TypeReg, Size: int64(len(data))}); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(data)
		}
		_ = w.Close()
		_ = gz.Close()
	} else {
		w := zip.NewWriter(&b)
		for _, name := range names {
			data := []byte("notice")
			if name == "minecraft-ui.exe" {
				data = []byte("binary")
			}
			f, e := w.Create(name)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = f.Write(data)
		}
		_ = w.Close()
	}
	return b.Bytes()
}
func stageFixture(t *testing.T, platform string, archive []byte) (Trust, []byte, []byte) {
	t.Helper()
	trust, m, _, _ := signedFixture(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	trust.PublicKeys["fixture"] = pub
	for i := range m.Artifacts {
		if m.Artifacts[i].OS == platform {
			m.Artifacts[i].Size = int64(len(archive))
			m.Artifacts[i].SHA256 = digest(archive)
		}
	}
	b, _ := json.Marshal(m)
	sig, _ := json.Marshal(Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, b))})
	return trust, b, sig
}
func TestStagingVerifiesBeforeDurablePublication(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			archive := archiveFixture(t, platform, []string{binaryName(platform), "LICENSE", "THIRD_PARTY_NOTICES.txt"})
			trust, b, sig := stageFixture(t, platform, archive)
			dir := t.TempDir()
			s := Stager{Dir: dir, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: platform, Arch: "amd64", Now: func() time.Time { return fixtureNow }}
			result, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"release.json", "release.sig", archiveName(platform), binaryName(platform), "ready.json"} {
				if _, err := os.Stat(filepath.Join(dir, result.ID, name)); err != nil {
					t.Fatal(err)
				}
			}
			got, _ := os.ReadFile(filepath.Join(dir, result.ID, binaryName(platform)))
			if string(got) != "binary" {
				t.Fatal(string(got))
			}
			if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive)); err == nil {
				t.Fatal("existing stage overwritten")
			}
		})
	}
}
func TestStagingRejectsArchiveAndHashFailuresWithoutReadyStage(t *testing.T) {
	for _, names := range [][]string{{"../minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"}, {"minecraft-ui", "minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"}, {"minecraft-ui", "surprise", "LICENSE", "THIRD_PARTY_NOTICES.txt"}, {"minecraft-ui"}} {
		archive := archiveFixture(t, "linux", names)
		trust, b, sig := stageFixture(t, "linux", archive)
		dir := t.TempDir()
		s := Stager{Dir: dir, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
		if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive)); err == nil {
			t.Fatal("unsafe archive accepted", names)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("failed stage retained", entries)
		}
	}
	archive := archiveFixture(t, "linux", []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	trust, b, sig := stageFixture(t, "linux", archive)
	s := Stager{Dir: t.TempDir(), Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
	if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(append(archive, 'x'))); err == nil {
		t.Fatal("size/hash mismatch accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Stage(ctx, "v1.2.3", b, sig, bytes.NewReader(archive)); err == nil {
		t.Fatal("cancellation ignored")
	}
}
func TestStagingLockAndFinalAuthorizationRefusePublication(t *testing.T) {
	archive := archiveFixture(t, "linux", []string{"minecraft-ui", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	trust, b, sig := stageFixture(t, "linux", archive)
	dir := t.TempDir()
	s := Stager{Dir: dir, Trust: trust, CurrentVersion: "v1.2.2", Schema: 16, OS: "linux", Arch: "amd64", Now: func() time.Time { return fixtureNow }}
	if err := os.WriteFile(filepath.Join(dir, ".staging.lock"), []byte("held"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive)); err == nil {
		t.Fatal("concurrent/stale lock ignored")
	}
	if err := os.Remove(filepath.Join(dir, ".staging.lock")); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.BeforePublish = func(context.Context) error { calls++; return errors.New("administrator revoked") }
	if _, err := s.Stage(context.Background(), "v1.2.3", b, sig, bytes.NewReader(archive)); err == nil || calls != 1 {
		t.Fatal("publication ignored final authorization", err, calls)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("revoked staged publication remained", entries)
	}
}
func TestReleaseZipDirectoryIsBoundedBeforeStandardParser(t *testing.T) {
	archive := archiveFixture(t, "windows", []string{"minecraft-ui.exe", "LICENSE", "THIRD_PARTY_NOTICES.txt"})
	file, err := os.CreateTemp(t.TempDir(), "zip")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := validateZipDirectory(file, int64(len(archive))); err != nil {
		t.Fatal(err)
	}
	end := append([]byte{}, archive[len(archive)-22:]...)
	binary.LittleEndian.PutUint32(end[12:16], 1<<20)
	binary.LittleEndian.PutUint32(end[16:20], 0)
	if _, err := file.WriteAt(end, int64(len(archive)-22)); err != nil {
		t.Fatal(err)
	}
	if err := validateZipDirectory(file, int64(len(archive))); err == nil {
		t.Fatal("unbounded/mismatched directory accepted")
	}
}
