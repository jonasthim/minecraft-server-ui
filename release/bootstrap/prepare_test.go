package bootstrap

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
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

type fixture struct {
	o         Options
	m         candidate.Bootstrap
	key       ed25519.PrivateKey
	body, sig []byte
	archives  map[string][]byte
	calls     []string
	hook      func(string)
	latest    *selfupdate.ReleaseSummary
	latestErr error
}

type snapshotCloseReader struct {
	io.Reader
	close func() error
}

func (r snapshotCloseReader) Close() error { return r.close() }

type snapshotSubstitutionSource struct {
	*fixture
	t           *testing.T
	replacement os.FileInfo
}

func (s *snapshotSubstitutionSource) BootstrapArchive(_ context.Context, _, platform string) (io.ReadCloser, error) {
	return snapshotCloseReader{Reader: bytes.NewReader(s.archives[platform]), close: func() error {
		path := filepath.Join(s.o.Output, ".bundle")
		if err := os.Remove(path); err != nil {
			s.t.Fatal(err)
		}
		if err := os.WriteFile(path, s.archives[platform], 0600); err != nil {
			s.t.Fatal(err)
		}
		var err error
		s.replacement, err = os.Lstat(path)
		if err != nil {
			s.t.Fatal(err)
		}
		return nil
	}}, nil
}

func TestSnapshotSubstitutionOnArchiveCloseRefused(t *testing.T) {
	f := newFixture(t)
	source := &snapshotSubstitutionSource{fixture: f, t: t}
	_, err := Prepare(context.Background(), f.o, source)
	path := filepath.Join(f.o.Output, ".bundle")
	now, statErr := os.Lstat(path)
	if err == nil || statErr != nil {
		t.Fatalf("Prepare error=%v; replacement stat=%v (want refusal and preserved replacement)", err, statErr)
	}
	if !strings.Contains(err.Error(), "cleanup incomplete; replaced or unremovable output state retained") {
		t.Fatal("retained replacement was not reported", err)
	}
	if !os.SameFile(now, source.replacement) {
		t.Fatal("replacement identity changed")
	}
	body, readErr := os.ReadFile(path)
	if readErr != nil || !bytes.Equal(body, f.archives["linux"]) {
		t.Fatal("replacement bytes changed", readErr)
	}
	assertAbsent(t, filepath.Join(f.o.Output, ReceiptName))
	entries, readErr := os.ReadDir(f.o.Output)
	if readErr != nil || len(entries) != 1 || entries[0].Name() != ".bundle" {
		t.Fatal("unexpected retained state", entries, readErr)
	}
}

func TestSnapshotReplacementBeforeOwnedRemovalPreserved(t *testing.T) {
	f := newFixture(t)
	d, err := newDestination(f.o.Output)
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	writer, err := d.create(".bundle", 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(f.archives["linux"]); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.openOwnedRegular(".bundle", candidate.MaxBundleBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err = candidate.VerifyBundle(context.Background(), f.m.Artifacts[0], snapshot); err != nil {
		t.Fatal(err)
	}
	if err = extract(context.Background(), d, f.m.Artifacts[0], snapshot); err != nil {
		t.Fatal(err)
	}
	// Verification and extraction already consumed the created snapshot. Replace
	// its pathname with a distinct, identically authenticated file before the
	// same ownership-aware removal that successful Prepare uses.
	if err = d.root.Remove(".bundle"); err != nil {
		t.Fatal(err)
	}
	foreign, err := d.root.OpenFile(".bundle", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Write(f.archives["linux"]); err != nil {
		foreign.Close()
		t.Fatal(err)
	}
	foreignInfo, err := foreign.Stat()
	if err != nil {
		foreign.Close()
		t.Fatal(err)
	}
	if err = foreign.Close(); err != nil {
		t.Fatal(err)
	}
	if err = d.removeOwned(".bundle"); err == nil {
		t.Fatal("removed a foreign replacement")
	}
	if err = d.cleanup(); err == nil {
		t.Fatal("retained replacement cleanup reported success")
	}
	now, err := d.root.Lstat(".bundle")
	if err != nil || !os.SameFile(now, foreignInfo) {
		t.Fatal("replacement was removed or changed", err)
	}
	body, err := d.root.ReadFile(".bundle")
	if err != nil || !bytes.Equal(body, f.archives["linux"]) {
		t.Fatal("replacement content changed", err)
	}
	entries, err := os.ReadDir(f.o.Output)
	if err != nil || len(entries) != 1 || entries[0].Name() != ".bundle" {
		t.Fatal("still-owned extracted state was not cleaned", entries, err)
	}
	assertAbsent(t, filepath.Join(f.o.Output, ReceiptName))
}

func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func wire(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}
func (f *fixture) sign() {
	f.body = wire(f.m)
	f.sig = wire(candidate.Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(f.key, f.body))})
}
func (f *fixture) call(s string) {
	f.calls = append(f.calls, s)
	if f.hook != nil {
		f.hook(s)
	}
}
func (f *fixture) Latest(context.Context) (*selfupdate.ReleaseSummary, error) {
	f.call("latest")
	return f.latest, f.latestErr
}
func (f *fixture) BootstrapMetadata(_ context.Context, v string) ([]byte, []byte, error) {
	f.call("metadata:" + v)
	return f.body, f.sig, nil
}
func (f *fixture) BootstrapArchive(_ context.Context, v, p string) (io.ReadCloser, error) {
	f.call("archive:" + v + ":" + p)
	return io.NopCloser(bytes.NewReader(f.archives[p])), nil
}

type entry struct {
	name string
	mode os.FileMode
	data []byte
	kind byte
}

func fixtureEntries(platform string) []entry {
	names := []string{"minecraft-ui", "selfupdate", "LICENSE", "THIRD_PARTY_NOTICES.txt", "install.sh", "unitctl", "minecraft@.service", "minecraft-ui.service", "sudoers", "minecraft-ui-update@.service", "minecraft-ui-update-recovery.service", "minecraft-ui-update.conf", "update-trust.example.json", "NOTICE"}
	if platform == "windows" {
		names = []string{"minecraft-ui.exe", "selfupdate.exe", "LICENSE", "THIRD_PARTY_NOTICES.txt", "install.ps1"}
	}
	var out []entry
	for i, n := range names {
		mode := os.FileMode(0644)
		if i < 2 || n == "install.sh" || n == "unitctl" {
			mode = 0755
		}
		if n == "sudoers" {
			mode = 0440
		}
		out = append(out, entry{n, mode, []byte("fixture bytes for " + n + "\n"), tar.TypeReg})
	}
	return out
}
func archive(t *testing.T, platform string, entries []entry) []byte {
	t.Helper()
	var b bytes.Buffer
	if platform == "linux" {
		g := gzip.NewWriter(&b)
		w := tar.NewWriter(g)
		for _, e := range entries {
			size := int64(len(e.data))
			link := ""
			if e.kind != tar.TypeReg {
				size = 0
				link = "/outside"
			}
			if err := w.WriteHeader(&tar.Header{Name: e.name, Mode: int64(e.mode.Perm()), Size: size, Typeflag: e.kind, Linkname: link, Format: tar.FormatUSTAR}); err != nil {
				t.Fatal(err)
			}
			if e.kind == tar.TypeReg {
				if _, err := w.Write(e.data); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		w := zip.NewWriter(&b)
		for _, e := range entries {
			h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			h.SetMode(e.mode)
			r, err := w.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	public, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	base := t.TempDir()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	f := &fixture{key: key, archives: map[string][]byte{}, o: Options{Repository: "example/manager", Version: "v1.2.3", TrustFile: filepath.Join(base, "trust.json"), Output: filepath.Join(base, "prepared"), OS: "linux", Arch: "amd64", Now: func() time.Time { return now }}}
	f.m = candidate.Bootstrap{Format: 1, Purpose: "minecraft-ui-bootstrap", Repository: f.o.Repository, Version: f.o.Version, PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Schema: 18, Trust: "OWNER SIGNED"}
	for _, p := range []string{"linux", "windows"} {
		es := fixtureEntries(p)
		payload := archive(t, p, es)
		f.archives[p] = payload
		name := "minecraft-ui-bootstrap-" + p + "-amd64.tar.gz"
		if p == "windows" {
			name = "minecraft-ui-bootstrap-windows-amd64.zip"
		}
		a := candidate.Bundle{OS: p, Arch: "amd64", Name: name, Size: int64(len(payload)), SHA256: hash(payload)}
		for _, e := range es {
			a.Members = append(a.Members, candidate.Member{Name: e.name, Size: int64(len(e.data)), SHA256: hash(e.data), Mode: uint32(e.mode.Perm())})
		}
		f.m.Artifacts = append(f.m.Artifacts, a)
	}
	f.sign()
	f.latest = &selfupdate.ReleaseSummary{Version: f.o.Version}
	trust := wire(map[string]any{"format": 1, "repository": f.o.Repository, "public_keys": map[string]string{"fixture": base64.StdEncoding.EncodeToString(public)}})
	if e = os.WriteFile(f.o.TrustFile, trust, 0600); e != nil {
		t.Fatal(e)
	}
	return f
}
func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, e := os.Lstat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("unexpected output state %q: %v", path, e)
	}
}

func TestPrepareExactAndLatestBothPlatforms(t *testing.T) {
	for _, p := range []string{"linux", "windows"} {
		for _, latest := range []bool{false, true} {
			t.Run(p+"/latest="+map[bool]string{true: "true", false: "false"}[latest], func(t *testing.T) {
				f := newFixture(t)
				f.o.OS = p
				if latest {
					f.o.Version = "latest"
				}
				r, e := Prepare(context.Background(), f.o, f)
				if e != nil {
					t.Fatal(e)
				}
				if r.Version != "v1.2.3" || r.OS != p || r.Schema != 18 || r.ManifestSHA256 != hash(f.body) {
					t.Fatalf("wrong receipt: %+v", r)
				}
				want := []string{"metadata:v1.2.3", "archive:v1.2.3:" + p}
				if latest {
					want = append([]string{"latest"}, want...)
				}
				if strings.Join(f.calls, ",") != strings.Join(want, ",") {
					t.Fatal(f.calls)
				}
				for _, m := range r.Members {
					data, e := os.ReadFile(filepath.Join(f.o.Output, m.Name))
					if e != nil || hash(data) != m.SHA256 {
						t.Fatalf("member %s: %v", m.Name, e)
					}
					if runtime.GOOS != "windows" {
						info, e := os.Stat(filepath.Join(f.o.Output, m.Name))
						if e != nil || info.Mode().Perm() != os.FileMode(m.Mode) {
							t.Fatalf("member mode %s: %v", m.Name, e)
						}
					}
				}
				data, e := os.ReadFile(filepath.Join(f.o.Output, ReceiptName))
				if e != nil || !bytes.Equal(data, wire(r)) {
					t.Fatal("completion receipt", e)
				}
				entries, e := os.ReadDir(f.o.Output)
				if e != nil || len(entries) != len(r.Members)+3 {
					t.Fatalf("temporary state remains: %v %v", entries, e)
				}
				if runtime.GOOS != "windows" {
					i, e := os.Stat(f.o.Output)
					if e != nil || i.Mode().Perm() != 0700 {
						t.Fatal("private mode", e)
					}
				}
			})
		}
	}
}

func TestPrepareRetainsExactAuthenticatedMetadata(t *testing.T) {
	f := newFixture(t)
	// The canonical authenticated inputs both include their required trailing
	// newline; preparation must preserve those exact bytes.
	metadataPath := filepath.Join(f.o.Output, "bootstrap.json")
	signaturePath := filepath.Join(f.o.Output, "bootstrap.sig")
	assertAbsent(t, metadataPath)
	assertAbsent(t, signaturePath)

	receipt, err := Prepare(context.Background(), f.o, f)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(metadataPath)
	if err != nil || !bytes.Equal(metadata, f.body) {
		t.Fatalf("retained metadata differs from authenticated input: %v", err)
	}
	signature, err := os.ReadFile(signaturePath)
	if err != nil || !bytes.Equal(signature, f.sig) {
		t.Fatalf("retained signature differs from authenticated input: %v", err)
	}
	publicTrust, err := os.ReadFile(f.o.TrustFile)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := candidate.ParsePublicTrust(publicTrust, f.o.Repository)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := candidate.VerifyBootstrap(trust, metadata, signature, f.o.Version, f.o.Now())
	if err != nil || verified.Version != receipt.Version {
		t.Fatalf("retained inputs failed independent trust verification: %v", err)
	}
	if receipt.ManifestSHA256 != hash(metadata) {
		t.Fatal("receipt digest does not cover retained metadata")
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{metadataPath, signaturePath} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("private metadata mode for %s: %v", filepath.Base(path), err)
			}
		}
	}
	entries, err := os.ReadDir(f.o.Output)
	if err != nil || len(entries) != len(receipt.Members)+3 {
		t.Fatalf("unexpected prepared outputs: %v (%v)", entries, err)
	}
	wantNames := map[string]bool{MetadataName: true, SignatureName: true, ReceiptName: true}
	for _, member := range receipt.Members {
		wantNames[member.Name] = true
	}
	for _, entry := range entries {
		if !wantNames[entry.Name()] {
			t.Fatalf("unexpected prepared output %q", entry.Name())
		}
		delete(wantNames, entry.Name())
	}
	if len(wantNames) != 0 {
		t.Fatalf("missing prepared outputs: %v", wantNames)
	}
	completion, err := os.ReadFile(filepath.Join(f.o.Output, ReceiptName))
	if err != nil || !bytes.Equal(completion, wire(receipt)) {
		t.Fatal("completion receipt mismatch", err)
	}
}

func TestPreparePreservesSubstitutedAuthenticatedInput(t *testing.T) {
	f := newFixture(t)
	var replacement os.FileInfo
	f.hook = func(call string) {
		if !strings.HasPrefix(call, "archive:") {
			return
		}
		path := filepath.Join(f.o.Output, MetadataName)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("foreign replacement"), 0600); err != nil {
			t.Fatal(err)
		}
		var err error
		replacement, err = os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Prepare(context.Background(), f.o, f); err == nil {
		t.Fatal("accepted substituted authenticated metadata")
	}
	path := filepath.Join(f.o.Output, MetadataName)
	info, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, replacement) {
		t.Fatal("replacement identity was removed or changed", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "foreign replacement" {
		t.Fatal("replacement bytes changed", err)
	}
	assertAbsent(t, filepath.Join(f.o.Output, SignatureName))
	assertAbsent(t, filepath.Join(f.o.Output, ReceiptName))
	entries, err := os.ReadDir(f.o.Output)
	if err != nil || len(entries) != 1 || entries[0].Name() != MetadataName {
		t.Fatal("unexpected retained state", entries, err)
	}
}

func TestAuthenticationRefusalsLeaveNoOutput(t *testing.T) {
	tests := map[string]func(*fixture){
		"repository": func(f *fixture) { f.m.Repository = "other/repo"; f.sign() },
		"version":    func(f *fixture) { f.m.Version = "v1.2.4"; f.sign() },
		"platform":   func(f *fixture) { f.m.Artifacts[0].Arch = "arm64"; f.sign() },
		"expiry":     func(f *fixture) { f.m.ExpiresAt = f.o.Now(); f.sign() },
		"future":     func(f *fixture) { f.m.PublishedAt = f.o.Now().Add(time.Minute); f.sign() },
		"signature":  func(f *fixture) { f.sig[len(f.sig)/2] ^= 1 },
		"public trust": func(f *fixture) {
			if e := os.WriteFile(f.o.TrustFile, []byte(`{"format":1,"repository":"evil/repo","public_keys":{}}`), 0600); e != nil {
				panic(e)
			}
		},
		"development default denied": func(f *fixture) { f.m.Trust = "DEVELOPMENT ONLY"; f.sign() },
		"metadata oversized":         func(f *fixture) { f.body = bytes.Repeat([]byte("x"), candidate.MaxBootstrapBytes+1) },
		"duplicate json": func(f *fixture) {
			f.body = append([]byte(`{"format":1,`), f.body[1:]...)
			f.sig = wire(candidate.Signature{KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(f.key, f.body))})
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			mutate(f)
			if _, e := Prepare(context.Background(), f.o, f); e == nil {
				t.Fatal("accepted invalid authentication")
			}
			assertAbsent(t, f.o.Output)
		})
	}
	t.Run("explicit development", func(t *testing.T) {
		f := newFixture(t)
		f.m.Trust = "DEVELOPMENT ONLY"
		f.sign()
		f.o.AllowDevelopment = true
		if r, e := Prepare(context.Background(), f.o, f); e != nil || r.Trust != "DEVELOPMENT ONLY" {
			t.Fatal(r, e)
		}
	})
}

func TestAuthenticatedMaliciousArchivesRejectedBeforeExtraction(t *testing.T) {
	for _, p := range []string{"linux", "windows"} {
		for _, mutation := range []string{"extra", "traversal", "absolute", "link", "duplicate", "reordered", "corrupt", "oversized", "member hash", "tail", "size overflow"} {
			t.Run(p+"/"+mutation, func(t *testing.T) {
				f := newFixture(t)
				f.o.OS = p
				index := 0
				if p == "windows" {
					index = 1
				}
				es := fixtureEntries(p)
				switch mutation {
				case "extra":
					es = append(es, entry{"unexpected", 0644, []byte("x"), tar.TypeReg})
				case "traversal":
					es[2].name = "../escape"
				case "absolute":
					es[2].name = "/escape"
				case "link":
					es[2].kind = tar.TypeSymlink
					es[2].mode |= os.ModeSymlink
				case "duplicate":
					es[2].name = es[0].name
				case "reordered":
					es[0], es[1] = es[1], es[0]
				case "oversized":
					es[2].data = bytes.Repeat([]byte("x"), int(candidate.MaxAssetBytes+1))
				case "member hash":
					f.m.Artifacts[index].Members[2].SHA256 = strings.Repeat("0", 64)
				}
				payload := archive(t, p, es)
				if mutation == "tail" {
					payload = append(payload, payload...)
				}
				f.m.Artifacts[index].Size = int64(len(payload))
				f.m.Artifacts[index].SHA256 = hash(payload)
				if mutation == "corrupt" {
					payload[len(payload)/2] ^= 1
				}
				if mutation == "size overflow" {
					payload = append(payload, 'x')
				}
				f.archives[p] = payload
				f.sign()
				if _, e := Prepare(context.Background(), f.o, f); e == nil {
					t.Fatal("accepted malicious archive")
				}
				assertAbsent(t, f.o.Output)
			})
		}
	}
}

func TestOutputNoClobberAndPathBinding(t *testing.T) {
	for _, kind := range []string{"file", "directory", "dangling", "parent symlink", "trust symlink", "trust parent symlink", "trust hardlink"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "file":
				if e := os.WriteFile(f.o.Output, []byte("retain"), 0600); e != nil {
					t.Fatal(e)
				}
			case "directory":
				if e := os.Mkdir(f.o.Output, 0700); e != nil {
					t.Fatal(e)
				}
			case "dangling":
				if e := os.Symlink(filepath.Join(t.TempDir(), "missing"), f.o.Output); e != nil {
					t.Skip(e)
				}
			case "parent symlink":
				link := filepath.Join(t.TempDir(), "link")
				if e := os.Symlink(filepath.Dir(f.o.Output), link); e != nil {
					t.Skip(e)
				}
				f.o.Output = filepath.Join(link, "prepared")
			case "trust symlink":
				link := filepath.Join(t.TempDir(), "trust")
				if e := os.Symlink(f.o.TrustFile, link); e != nil {
					t.Skip(e)
				}
				f.o.TrustFile = link
			case "trust parent symlink":
				link := filepath.Join(t.TempDir(), "link")
				if e := os.Symlink(filepath.Dir(f.o.TrustFile), link); e != nil {
					t.Skip(e)
				}
				f.o.TrustFile = filepath.Join(link, "trust.json")
			case "trust hardlink":
				if e := os.Link(f.o.TrustFile, filepath.Join(t.TempDir(), "alias")); e != nil {
					t.Skip(e)
				}
			}
			if _, e := Prepare(context.Background(), f.o, f); e == nil {
				t.Fatal("unsafe path accepted")
			}
			if kind == "file" {
				b, e := os.ReadFile(f.o.Output)
				if e != nil || string(b) != "retain" {
					t.Fatal("clobbered existing output")
				}
			}
			if kind == "dangling" {
				if _, e := os.Readlink(f.o.Output); e != nil {
					t.Fatal("clobbered link")
				}
			}
		})
	}
	t.Run("destination replacement not removed", func(t *testing.T) {
		f := newFixture(t)
		moved := f.o.Output + "-moved"
		f.hook = func(call string) {
			if !strings.HasPrefix(call, "archive:") {
				return
			}
			if e := os.Rename(f.o.Output, moved); e != nil {
				t.Fatal(e)
			}
			if e := os.Mkdir(f.o.Output, 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(f.o.Output, "retain"), []byte("mine"), 0600); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := Prepare(context.Background(), f.o, f); e == nil {
			t.Fatal("accepted replacement")
		}
		b, e := os.ReadFile(filepath.Join(f.o.Output, "retain"))
		if e != nil || string(b) != "mine" {
			t.Fatal("replacement touched", e)
		}
		es, e := os.ReadDir(moved)
		if e != nil || len(es) != 0 {
			t.Fatal("original destination cleanup", es, e)
		}
	})
	for _, cancelAfterAttempt := range []bool{false, true} {
		name := "parent substitution detected with retained cleanup"
		if cancelAfterAttempt {
			name = "parent rename cancellation with retained cleanup"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			parent := filepath.Join(t.TempDir(), "parent")
			if e := os.Mkdir(parent, 0700); e != nil {
				t.Fatal(e)
			}
			originalParent, e := os.Lstat(parent)
			if e != nil {
				t.Fatal(e)
			}
			guardPath := filepath.Join(parent, "caller-owned.txt")
			if e = os.WriteFile(guardPath, []byte("preserve caller data"), 0600); e != nil {
				t.Fatal(e)
			}
			originalGuard, e := os.Lstat(guardPath)
			if e != nil {
				t.Fatal(e)
			}
			f.o.Output = filepath.Join(parent, "prepared")
			moved := parent + "-moved"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempted, denied := false, false
			var originalOutput os.FileInfo
			f.hook = func(call string) {
				if !strings.HasPrefix(call, "archive:") {
					return
				}
				attempted = true
				var err error
				originalOutput, err = os.Lstat(f.o.Output)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(parent, moved); err != nil {
					// Windows can reject renaming an ancestor while a descendant
					// directory handle is retained. Accept only that permission
					// outcome, and prove below that rename works after release.
					if runtime.GOOS != "windows" || !os.IsPermission(err) {
						t.Fatal(err)
					}
					denied = true
					assertAbsent(t, moved)
					for path, want := range map[string]os.FileInfo{parent: originalParent, f.o.Output: originalOutput, guardPath: originalGuard} {
						now, statErr := os.Lstat(path)
						if statErr != nil || !os.SameFile(now, want) {
							t.Fatalf("denied rename changed identity: %s: %v", path, statErr)
						}
					}
				} else {
					if err = os.Mkdir(parent, 0700); err != nil {
						t.Fatal(err)
					}
					if err = os.WriteFile(filepath.Join(parent, "retain"), []byte("mine"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if cancelAfterAttempt {
					cancel()
				}
			}
			receipt, prepareErr := Prepare(ctx, f.o, f)
			if !attempted {
				t.Fatal("parent rename was not attempted")
			}
			if !denied {
				if prepareErr == nil {
					t.Fatal("accepted substituted parent")
				}
				assertAbsent(t, filepath.Join(moved, "prepared"))
				b, err := os.ReadFile(filepath.Join(parent, "retain"))
				if err != nil || string(b) != "mine" {
					t.Fatal("replacement touched", err)
				}
				guard, err := os.ReadFile(filepath.Join(moved, "caller-owned.txt"))
				if err != nil || string(guard) != "preserve caller data" {
					t.Fatal("caller data changed during retained cleanup", err)
				}
				return
			}
			assertAbsent(t, moved)
			if cancelAfterAttempt {
				if prepareErr == nil {
					t.Fatal("cancelled preparation succeeded after denied rename")
				}
				assertAbsent(t, f.o.Output)
				entries, err := os.ReadDir(parent)
				if err != nil || len(entries) != 1 || entries[0].Name() != "caller-owned.txt" {
					t.Fatal("cancelled preparation left owned state", entries, err)
				}
			} else {
				if prepareErr != nil {
					t.Fatal("denied rename prevented safe preparation", prepareErr)
				}
				now, err := os.Lstat(f.o.Output)
				if err != nil || !os.SameFile(now, originalOutput) {
					t.Fatal("output identity changed", err)
				}
				if receipt.Version != f.o.Version || receipt.ManifestSHA256 != hash(f.body) {
					t.Fatal("unexpected preparation receipt", receipt)
				}
				body, err := os.ReadFile(filepath.Join(f.o.Output, ReceiptName))
				if err != nil || !bytes.Equal(body, wire(receipt)) {
					t.Fatal("completion receipt mismatch", err)
				}
				entries, err := os.ReadDir(f.o.Output)
				if err != nil || len(entries) != len(receipt.Members)+3 {
					t.Fatal("unexpected completed output state", entries, err)
				}
				for _, member := range receipt.Members {
					body, err := os.ReadFile(filepath.Join(f.o.Output, member.Name))
					if err != nil || hash(body) != member.SHA256 {
						t.Fatal("prepared member mismatch", member.Name, err)
					}
				}
				assertAbsent(t, filepath.Join(f.o.Output, ".bundle"))
			}
			now, err := os.Lstat(parent)
			if err != nil || !os.SameFile(now, originalParent) {
				t.Fatal("parent identity changed", err)
			}
			now, err = os.Lstat(guardPath)
			if err != nil || !os.SameFile(now, originalGuard) {
				t.Fatal("caller file identity changed", err)
			}
			guard, err := os.ReadFile(guardPath)
			if err != nil || string(guard) != "preserve caller data" {
				t.Fatal("caller data changed after denied rename", err)
			}
			// A persistent ACL/privilege denial or leaked handle must not pass:
			// exactly the same rename must work once Prepare closes its roots.
			if err = os.Rename(parent, moved); err != nil {
				t.Fatal("parent rename remained denied after preparation released handles", err)
			}
			assertAbsent(t, parent)
			now, err = os.Lstat(moved)
			if err != nil || !os.SameFile(now, originalParent) {
				t.Fatal("post-release rename changed identity", err)
			}
			if err = os.Rename(moved, parent); err != nil {
				t.Fatal(err)
			}
			t.Logf("Windows refused parent rename while roots were retained; preparation cancel=%t and post-release rename verified", cancelAfterAttempt)
		})
	}
}

func TestCancellationAndLatestFailures(t *testing.T) {
	t.Run("before IO", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := Prepare(ctx, f.o, f); e == nil {
			t.Fatal("cancel ignored")
		}
		if len(f.calls) != 0 {
			t.Fatal("performed IO", f.calls)
		}
		assertAbsent(t, f.o.Output)
	})
	t.Run("during archive", func(t *testing.T) {
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		f.hook = func(s string) {
			if strings.HasPrefix(s, "archive:") {
				for name, want := range map[string][]byte{MetadataName: f.body, SignatureName: f.sig} {
					got, readErr := os.ReadFile(filepath.Join(f.o.Output, name))
					if readErr != nil || !bytes.Equal(got, want) {
						t.Fatalf("authenticated input %s was not retained before archive acquisition: %v", name, readErr)
					}
				}
				cancel()
			}
		}
		if _, e := Prepare(ctx, f.o, f); e == nil {
			t.Fatal("cancel ignored")
		}
		assertAbsent(t, f.o.Output)
	})
	for _, network := range []bool{false, true} {
		t.Run("latest failure", func(t *testing.T) {
			f := newFixture(t)
			f.o.Version = "latest"
			f.latest = nil
			if network {
				f.latestErr = errors.New("https://signed.invalid/?token=secret")
			}
			_, e := Prepare(context.Background(), f.o, f)
			if e == nil || strings.Contains(e.Error(), "secret") || len(f.calls) != 1 {
				t.Fatal(e, f.calls)
			}
			assertAbsent(t, f.o.Output)
		})
	}
}

func TestOfflineExactArtifactsAndValidation(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()
	f.o.Artifacts = dir
	for name, body := range map[string][]byte{"bootstrap.json": f.body, "bootstrap.sig": f.sig, "minecraft-ui-bootstrap-linux-amd64.tar.gz": f.archives["linux"]} {
		if e := os.WriteFile(filepath.Join(dir, name), body, 0600); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := Prepare(context.Background(), f.o, nil); e != nil {
		t.Fatal(e)
	}
	f.o.Output = filepath.Join(t.TempDir(), "new")
	f.o.Version = "latest"
	if _, e := Prepare(context.Background(), f.o, nil); e == nil {
		t.Fatal("offline latest allowed")
	}
	assertAbsent(t, f.o.Output)
	for _, mutate := range []func(*Options){func(o *Options) { o.Repository = "evil/../repo" }, func(o *Options) { o.Version = "v1.2.3?token=secret" }, func(o *Options) { o.Output = "relative" }, func(o *Options) { o.TrustFile = "relative" }, func(o *Options) { o.OS = "darwin" }, func(o *Options) { o.Arch = "arm64" }} {
		f := newFixture(t)
		mutate(&f.o)
		if _, e := Prepare(context.Background(), f.o, f); e == nil {
			t.Fatal("invalid options accepted")
		}
		if len(f.calls) != 0 {
			t.Fatal("I/O before validation")
		}
	}
}

func TestExtractionRechecksAndNeverUsesArchivePaths(t *testing.T) {
	f := newFixture(t)
	d, e := newDestination(f.o.Output)
	if e != nil {
		t.Fatal(e)
	}
	defer d.close()
	defer d.cleanup()
	es := fixtureEntries("linux")
	es[0].name = "../escape"
	payload := archive(t, "linux", es)
	p := filepath.Join(t.TempDir(), "snapshot")
	if e = os.WriteFile(p, payload, 0600); e != nil {
		t.Fatal(e)
	}
	file, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	if e = extract(context.Background(), d, f.m.Artifacts[0], file); e == nil {
		t.Fatal("extraction trusted changed names")
	}
	assertAbsent(t, filepath.Join(filepath.Dir(f.o.Output), "escape"))
}

func TestShippingCommandOfflineSuccessWithoutExecutingMembers(t *testing.T) {
	if runtime.GOARCH != "amd64" || (runtime.GOOS != "linux" && runtime.GOOS != "windows") {
		t.Skip("shipping command is native linux/windows amd64 only")
	}
	f := newFixture(t)
	f.o.OS = runtime.GOOS
	f.m.PublishedAt = time.Now().UTC().Add(-time.Hour)
	f.m.ExpiresAt = time.Now().UTC().Add(time.Hour)
	// A manager member that would create a marker if invoked must remain data.
	marker := filepath.Join(t.TempDir(), "EXECUTED")
	if runtime.GOOS == "linux" {
		es := fixtureEntries("linux")
		es[0].data = []byte("#!/bin/sh\nprintf executed > '" + marker + "'\n")
		f.archives["linux"] = archive(t, "linux", es)
		a := &f.m.Artifacts[0]
		a.Size = int64(len(f.archives["linux"]))
		a.SHA256 = hash(f.archives["linux"])
		a.Members[0].Size = int64(len(es[0].data))
		a.Members[0].SHA256 = hash(es[0].data)
	}
	f.sign()
	dir := t.TempDir()
	for name, b := range map[string][]byte{"bootstrap.json": f.body, "bootstrap.sig": f.sig, f.m.Artifacts[map[string]int{"linux": 0, "windows": 1}[runtime.GOOS]].Name: f.archives[runtime.GOOS]} {
		if e := os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	tool, e := filepath.Abs("../../cmd/minecraft-ui-bootstrap")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("go", "run", tool, "prepare", "--repository", f.o.Repository, "--trust-file", f.o.TrustFile, "--version", f.o.Version, "--output", f.o.Output, "--artifacts", dir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if e = cmd.Run(); e != nil {
		t.Fatalf("shipping command: %v %s", e, stderr.String())
	}
	if stderr.Len() != 0 || stdout.Len() > 16<<10 {
		t.Fatal("unexpected command output", stderr.String())
	}
	var r Receipt
	if e = json.Unmarshal(stdout.Bytes(), &r); e != nil || r.Version != f.o.Version {
		t.Fatal("receipt", r, e)
	}
	assertAbsent(t, marker)
}
