package candidate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

// These fixtures are actual Go ELF/PE executables with the same linker stamp
// contract as native CI builds. Cross compilation here is format evidence only.
func fixture(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/jonasthim/minecraft-server-ui\n\ngo 1.27.0\n")
	for _, command := range []string{"minecraft-ui", "minecraft-ui-update"} {
		write("cmd/"+command+"/main.go", "package main\nimport \"fmt\"\nvar version = \"dev\"\nfunc main(){fmt.Println(version)}\n")
	}
	cfg := Config{SourceRoot: root, Version: "v0.0.1", Repository: "jonasthim/minecraft-server-ui", Schema: 16, PublishedAt: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC), KeyID: "fixture", Development: true}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SigningKey = key
	for _, target := range []struct {
		platform, command string
		out               *string
	}{{"linux", "minecraft-ui", &cfg.LinuxManager}, {"windows", "minecraft-ui", &cfg.WindowsManager}, {"linux", "minecraft-ui-update", &cfg.LinuxUpdater}, {"windows", "minecraft-ui-update", &cfg.WindowsUpdater}} {
		p := filepath.Join(root, target.platform+"-"+target.command)
		c := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w -X main.version="+cfg.Version, "-o", p, "./cmd/"+target.command)
		c.Dir = root
		c.Env = append(os.Environ(), "GOOS="+target.platform, "GOARCH=amd64", "CGO_ENABLED=0", "GOWORK=off")
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("fixture build: %v: %s", err, b)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		schema := cfg.Schema
		if target.command == "minecraft-ui-update" {
			schema = 0
		}
		receipt, _ := json.Marshal(VersionReceipt{Format: 1, OS: target.platform, Arch: "amd64", Command: target.command, Version: cfg.Version, BinarySHA256: digest(data), Schema: schema})
		if err := os.WriteFile(p+".version.json", receipt, 0600); err != nil {
			t.Fatal(err)
		}
		*target.out = p
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.txt"} {
		write(name, "fixture license\n")
	}
	for _, a := range linuxAssets {
		write("deploy/linux/"+a.name, "fixture "+a.name+"\n")
	}
	for _, a := range windowsAssets {
		write("deploy/windows/"+a.name, "fixture "+a.name+"\n")
	}
	return cfg
}
func produce(t *testing.T, cfg Config) string {
	t.Helper()
	cfg.Output = filepath.Join(t.TempDir(), "candidate")
	if err := Produce(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Output
}
func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(dir, name))
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func trusted(cfg Config) selfupdate.Trust {
	return selfupdate.Trust{Repository: cfg.Repository, PublicKeys: map[string]ed25519.PublicKey{cfg.KeyID: cfg.SigningKey.Public().(ed25519.PublicKey)}}
}

func TestCandidateContract(t *testing.T) {
	cfg := fixture(t)
	dir := produce(t, cfg)
	t.Run("shipping verifier and stager accept both update archives", func(t *testing.T) {
		body, sig := read(t, dir, "release.json"), read(t, dir, "release.sig")
		manifest, err := selfupdate.VerifyManifest(trusted(cfg), body, sig, cfg.Version, "0.0.0-dev", cfg.Schema, cfg.PublishedAt)
		if err != nil {
			t.Fatal(err)
		}
		for _, platform := range []string{"linux", "windows"} {
			artifact, err := manifest.Artifact(platform, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			stageRoot := t.TempDir()
			s := selfupdate.Stager{Dir: stageRoot, Trust: trusted(cfg), CurrentVersion: "0.0.0-dev", Schema: cfg.Schema, OS: platform, Arch: "amd64", Now: func() time.Time { return cfg.PublishedAt }}
			staged, err := s.Stage(context.Background(), cfg.Version, body, sig, bytes.NewReader(read(t, dir, artifact.Name)))
			if err != nil {
				t.Fatal(err)
			}
			if staged.Version != cfg.Version {
				t.Fatal(staged)
			}
			name := "minecraft-ui"
			source := cfg.LinuxManager
			if platform == "windows" {
				name += ".exe"
				source = cfg.WindowsManager
			}
			want, e := os.ReadFile(source)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(read(t, filepath.Join(stageRoot, staged.ID), name), want) {
				t.Fatal("staged executable differs")
			}
		}
	})

	t.Run("actual packaging CLI supplied private input and ephemeral development modes", func(t *testing.T) {
		tool, e := filepath.Abs("../cmd/package")
		if e != nil {
			t.Fatal(e)
		}
		for _, development := range []bool{false, true} {
			output := filepath.Join(t.TempDir(), "cli-candidate")
			args := []string{"run", tool, "--source", cfg.SourceRoot, "--out", output, "--version", cfg.Version, "--repository", cfg.Repository, "--schema", "16", "--published-at", cfg.PublishedAt.Format(time.RFC3339), "--linux-manager", cfg.LinuxManager, "--windows-manager", cfg.WindowsManager, "--linux-updater", cfg.LinuxUpdater, "--windows-updater", cfg.WindowsUpdater}
			if development {
				args = append(args, "--development")
			} else {
				args = append(args, "--key-id", cfg.KeyID, "--signing-key-file", "-")
			}
			command := exec.Command("go", args...)
			command.Stdin = bytes.NewReader(cfg.SigningKey)
			log, e := command.CombinedOutput()
			if e != nil {
				t.Fatalf("CLI: %v %s", e, log)
			}
			if bytes.Contains(log, cfg.SigningKey) {
				t.Fatal("CLI leaked private material")
			}
			expected := 8
			trust := trusted(cfg)
			if development {
				expected = 10
				var fixtureTrust struct {
					PublicKeys map[string]string `json:"public_keys"`
				}
				if e = json.Unmarshal(read(t, output, "development-trust.json"), &fixtureTrust); e != nil {
					t.Fatal(e)
				}
				trust.PublicKeys = map[string]ed25519.PublicKey{}
				for id, key := range fixtureTrust.PublicKeys {
					decoded, e := base64.StdEncoding.Strict().DecodeString(key)
					if e != nil {
						t.Fatal(e)
					}
					trust.PublicKeys[id] = decoded
				}
			}
			entries, e := os.ReadDir(output)
			if e != nil || len(entries) != expected {
				t.Fatalf("CLI output count %d want %d: %v", len(entries), expected, e)
			}
			if _, e := selfupdate.VerifyManifest(trust, read(t, output, "release.json"), read(t, output, "release.sig"), cfg.Version, "0.0.0-dev", cfg.Schema, cfg.PublishedAt); e != nil {
				t.Fatal(e)
			}
			trustPath := filepath.Join(output, "development-trust.json")
			if !development {
				trustPath = filepath.Join(t.TempDir(), "owner-public-trust.json")
				public := map[string]string{}
				for id, key := range trust.PublicKeys {
					public[id] = base64.StdEncoding.EncodeToString(key)
				}
				body, _ := json.Marshal(map[string]any{"format": 1, "repository": cfg.Repository, "public_keys": public})
				if e = os.WriteFile(trustPath, body, 0600); e != nil {
					t.Fatal(e)
				}
			}
			verifier, e := filepath.Abs("../cmd/verify")
			if e != nil {
				t.Fatal(e)
			}
			verifyArgs := []string{"run", verifier, "--artifacts", output, "--trust-file", trustPath, "--repository", cfg.Repository, "--version", cfg.Version, "--schema", "16", "--now", cfg.PublishedAt.Format(time.RFC3339)}
			verify := exec.Command("go", verifyArgs...)
			if log, e = verify.CombinedOutput(); e != nil || !strings.Contains(string(log), "both shipping update stages and bootstrap bundles") {
				t.Fatalf("artifact verification CLI: %v %s", e, log)
			}
			corruptName := "minecraft-ui-linux-amd64.tar.gz"
			if development {
				corruptName = "minecraft-ui-bootstrap-windows-amd64.zip"
			}
			corrupt := read(t, output, corruptName)
			corrupt[len(corrupt)/2] ^= 1
			if e = os.WriteFile(filepath.Join(output, corruptName), corrupt, 0600); e != nil {
				t.Fatal(e)
			}
			verify = exec.Command("go", verifyArgs...)
			if log, e = verify.CombinedOutput(); e == nil {
				t.Fatalf("artifact CLI accepted tampered %s: %s", corruptName, log)
			}

		}
	})
	t.Run("deterministic bytes and public development trust only", func(t *testing.T) {
		// Source filesystem modes and timestamps must never enter an archive.
		if e := filepath.WalkDir(cfg.SourceRoot, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if e = os.Chmod(path, 0600); e != nil {
				return e
			}
			return os.Chtimes(path, cfg.PublishedAt, cfg.PublishedAt)
		}); e != nil {
			t.Fatal(e)
		}
		again := produce(t, cfg)
		entries, e := os.ReadDir(dir)
		if e != nil {
			t.Fatal(e)
		}
		if len(entries) != 10 {
			t.Fatalf("unexpected outputs: %v", entries)
		}
		for _, entry := range entries {
			a, b := read(t, dir, entry.Name()), read(t, again, entry.Name())
			if !bytes.Equal(a, b) {
				t.Fatalf("not reproducible: %s", entry.Name())
			}
			if bytes.Contains(a, cfg.SigningKey) || bytes.Contains(a, cfg.SigningKey.Seed()) {
				t.Fatal("private key leaked")
			}
		}
		if !strings.Contains(string(read(t, dir, "DEVELOPMENT-ONLY.txt")), "DEVELOPMENT ONLY") {
			t.Fatal("missing trust disclaimer")
		}
	})
	t.Run("bootstrap verifies exact14linux5windows allowlist", func(t *testing.T) {
		b, s := read(t, dir, "bootstrap.json"), read(t, dir, "bootstrap.sig")
		m, e := VerifyBootstrap(Trust(trusted(cfg)), b, s, cfg.Version, cfg.PublishedAt)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range m.Artifacts {
			want := 14
			if a.OS == "windows" {
				want = 5
			}
			if len(a.Members) != want {
				t.Fatalf("%s has %d members", a.OS, len(a.Members))
			}
			if e := VerifyBundle(context.Background(), a, bytes.NewReader(read(t, dir, a.Name))); e != nil {
				t.Fatal(e)
			}
		}
	})
	t.Run("bootstrap tamper identity schema and expiry fail closed", func(t *testing.T) {
		b, s := read(t, dir, "bootstrap.json"), read(t, dir, "bootstrap.sig")
		mutations := []func(*Bootstrap){func(m *Bootstrap) { m.Repository = "elsewhere/repo" }, func(m *Bootstrap) { m.Version = "v0.0.2" }, func(m *Bootstrap) { m.Purpose = "another-purpose" }, func(m *Bootstrap) { m.Schema = 0 }, func(m *Bootstrap) { m.ExpiresAt = m.PublishedAt.Add(31 * 24 * time.Hour) }, func(m *Bootstrap) { m.Artifacts[0].Members = append(m.Artifacts[0].Members, m.Artifacts[0].Members[0]) }, func(m *Bootstrap) { m.Artifacts[0].Members[0].Name = "../escape" }, func(m *Bootstrap) { m.Artifacts[0].Members[0].Mode = 04755 }}
		for i, mutate := range mutations {
			var m Bootstrap
			if e := json.Unmarshal(b, &m); e != nil {
				t.Fatal(e)
			}
			mutate(&m)
			changed, _ := canonical(m)
			sig, _ := sign(changed, cfg.KeyID, cfg.SigningKey)
			if _, e := VerifyBootstrap(Trust(trusted(cfg)), changed, sig, cfg.Version, cfg.PublishedAt); e == nil {
				t.Fatalf("mutation %d accepted", i)
			}
		}
		corrupt := append([]byte(nil), b...)
		corrupt[len(corrupt)/2] ^= 1
		if _, e := VerifyBootstrap(Trust(trusted(cfg)), corrupt, s, cfg.Version, cfg.PublishedAt); e == nil {
			t.Fatal("tampered metadata accepted")
		}
		if _, e := VerifyBootstrap(Trust(trusted(cfg)), b, s, cfg.Version, cfg.PublishedAt.Add(30*24*time.Hour)); e == nil {
			t.Fatal("expired metadata accepted")
		}
		if _, e := VerifyBootstrap(Trust{Repository: cfg.Repository, PublicKeys: map[string]ed25519.PublicKey{"other": cfg.SigningKey.Public().(ed25519.PublicKey)}}, b, s, cfg.Version, cfg.PublishedAt); e == nil {
			t.Fatal("untrusted key accepted")
		}
	})
	t.Run("archive corruption and signed unsafe extra files rejected", func(t *testing.T) {
		b, s := read(t, dir, "bootstrap.json"), read(t, dir, "bootstrap.sig")
		m, e := VerifyBootstrap(Trust(trusted(cfg)), b, s, cfg.Version, cfg.PublishedAt)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range m.Artifacts {
			payload := read(t, dir, a.Name)
			payload[len(payload)/2] ^= 1
			if e := VerifyBundle(context.Background(), a, bytes.NewReader(payload)); e == nil {
				t.Fatal("tampered bundle accepted")
			}
		}
		releaseBody, releaseSig := read(t, dir, "release.json"), read(t, dir, "release.sig")
		var rm selfupdate.Manifest
		if e := json.Unmarshal(releaseBody, &rm); e != nil {
			t.Fatal(e)
		}
		for _, platform := range []string{"linux", "windows"} {
			for _, badName := range []string{"extra", "../escape", "/absolute", "sub/file", "minecraft-ui"} {
				data := addArchiveMember(t, platform, read(t, dir, archiveName(platform, false)), badName)
				altered := rm
				altered.Artifacts = append([]selfupdate.Artifact(nil), rm.Artifacts...)
				for i := range altered.Artifacts {
					if altered.Artifacts[i].OS == platform {
						altered.Artifacts[i].Size = int64(len(data))
						altered.Artifacts[i].SHA256 = digest(data)
					}
				}
				body, _ := json.Marshal(altered)
				sig, _ := sign(body, cfg.KeyID, cfg.SigningKey)
				stager := selfupdate.Stager{Dir: t.TempDir(), Trust: trusted(cfg), CurrentVersion: "0.0.0-dev", Schema: cfg.Schema, OS: platform, Arch: "amd64", Now: func() time.Time { return cfg.PublishedAt }}
				if _, err := stager.Stage(context.Background(), cfg.Version, body, sig, bytes.NewReader(data)); err == nil {
					t.Fatalf("accepted unsafe update archive %s %s", platform, badName)
				}
			}
		}
		if _, e := selfupdate.VerifyManifest(trusted(cfg), releaseBody, releaseSig, "v0.0.2", "0.0.0-dev", cfg.Schema, cfg.PublishedAt); e == nil {
			t.Fatal("wrong release tag accepted")
		}
	})

	t.Run("authenticated bootstrap archive member rules", func(t *testing.T) {
		m, e := VerifyBootstrap(Trust(trusted(cfg)), read(t, dir, "bootstrap.json"), read(t, dir, "bootstrap.sig"), cfg.Version, cfg.PublishedAt)
		if e != nil {
			t.Fatal(e)
		}
		for _, a := range m.Artifacts {
			for _, mutation := range []string{"extra", "traversal", "symlink", "duplicate", "member hash", "tail", "oversized"} {
				t.Run(a.OS+"/"+mutation, func(t *testing.T) {
					original := read(t, dir, a.Name)
					all := entries(t, a.OS, original)
					altered := a
					altered.Members = append([]Member(nil), a.Members...)
					switch mutation {
					case "extra":
						all = append(all, testEntry{"unexpected", 0644, []byte("x")})
					case "traversal":
						all[2].name = "../LICENSE"
					case "symlink":
						all[2].mode = os.ModeSymlink | 0644
					case "duplicate":
						all[2].name = all[0].name
					case "member hash":
						altered.Members[2].SHA256 = strings.Repeat("0", 64)
					case "oversized":
						all[2].data = bytes.Repeat([]byte("x"), int(MaxAssetBytes+1))
					}
					payload := testArchive(t, a.OS, all)
					if mutation == "tail" {
						payload = append(payload, original...)
					}
					altered.Size = int64(len(payload))
					altered.SHA256 = digest(payload)
					if e := VerifyBundle(context.Background(), altered, bytes.NewReader(payload)); e == nil {
						t.Fatalf("accepted %s bootstrap %s", a.OS, mutation)
					}
				})
			}
		}
	})

	t.Run("zip local header names must match authenticated central names", func(t *testing.T) {
		m, e := VerifyBootstrap(Trust(trusted(cfg)), read(t, dir, "bootstrap.json"), read(t, dir, "bootstrap.sig"), cfg.Version, cfg.PublishedAt)
		if e != nil {
			t.Fatal(e)
		}
		a := m.Artifacts[1]
		payload := read(t, dir, a.Name)
		copy(payload[30:30+len(a.Members[0].Name)], []byte("../escape______"))
		a.Size = int64(len(payload))
		a.SHA256 = digest(payload)
		if e = VerifyBundle(context.Background(), a, bytes.NewReader(payload)); e == nil {
			t.Fatal("accepted ZIP whose local name differs from authenticated central name")
		}
	})
	t.Run("version receipts bind bytes native identity and schema", func(t *testing.T) {
		data, e := os.ReadFile(cfg.LinuxManager)
		if e != nil {
			t.Fatal(e)
		}
		base := VersionReceipt{Format: 1, OS: "linux", Arch: "amd64", Command: "minecraft-ui", Version: cfg.Version, BinarySHA256: digest(data), Schema: cfg.Schema}
		for _, mutation := range []string{"missing", "hash", "version", "schema", "platform", "command", "duplicate", "unknown"} {
			t.Run(mutation, func(t *testing.T) {
				c := cfg
				c.Output = filepath.Join(t.TempDir(), "candidate")
				c.LinuxManager = filepath.Join(t.TempDir(), "manager")
				if e := os.WriteFile(c.LinuxManager, data, 0600); e != nil {
					t.Fatal(e)
				}
				r := base
				switch mutation {
				case "hash":
					r.BinarySHA256 = strings.Repeat("0", 64)
				case "version":
					r.Version = "v0.0.2"
				case "schema":
					r.Schema++
				case "platform":
					r.OS = "windows"
				case "command":
					r.Command = "minecraft-ui-update"
				}
				body, _ := json.Marshal(r)
				if mutation == "duplicate" {
					body = append([]byte(`{"format":1,`), body[1:]...)
				}
				if mutation == "unknown" {
					body = append([]byte(`{"unknown":1,`), body[1:]...)
				}
				if mutation != "missing" {
					if e := os.WriteFile(c.LinuxManager+".version.json", body, 0600); e != nil {
						t.Fatal(e)
					}
				}
				if e := Produce(context.Background(), c); e == nil || !strings.Contains(e.Error(), "receipt") {
					t.Fatal("invalid receipt did not fail specifically", e)
				}
				if _, e := os.Stat(c.Output); !os.IsNotExist(e) {
					t.Fatal("failed receipt left output")
				}
			})
		}
	})
	t.Run("unsafe source files and wrong executable identity leave no output", func(t *testing.T) {
		tests := []struct {
			name   string
			change func(*Config)
		}{
			{"version", func(c *Config) { c.Version = "v0.0.2" }},
			{"platform", func(c *Config) { c.WindowsManager = c.LinuxManager }},
			{"command", func(c *Config) { c.LinuxManager = c.LinuxUpdater }},
			{"symlink", func(c *Config) {
				p := filepath.Join(t.TempDir(), "link")
				if e := os.Symlink(c.LinuxManager, p); e != nil {
					t.Skip(e)
				}
				c.LinuxManager = p
			}},
			{"special file", func(c *Config) { c.LinuxManager = t.TempDir() }},
			{"oversized source", func(c *Config) {
				p := filepath.Join(t.TempDir(), "large")
				f, e := os.Create(p)
				if e != nil {
					t.Fatal(e)
				}
				if e = f.Truncate(MaxBinaryBytes + 1); e != nil {
					t.Fatal(e)
				}
				if e = f.Close(); e != nil {
					t.Fatal(e)
				}
				c.LinuxManager = p
			}},
			{"parent symlink", func(c *Config) {
				p := filepath.Join(t.TempDir(), "source-parent")
				if e := os.Symlink(filepath.Dir(c.LinuxManager), p); e != nil {
					t.Skip(e)
				}
				c.LinuxManager = filepath.Join(p, filepath.Base(c.LinuxManager))
			}},
			{"version overflow", func(c *Config) { c.Version = "v18446744073709551616.0.0" }},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				c := cfg
				c.Output = filepath.Join(t.TempDir(), "candidate")
				tt.change(&c)
				if e := Produce(context.Background(), c); e == nil {
					t.Fatal("unsafe source accepted")
				}
				if _, e := os.Lstat(c.Output); !os.IsNotExist(e) {
					t.Fatal("failure left output", e)
				}
			})
		}
	})
	t.Run("output no clobber and cancellation", func(t *testing.T) {
		original := read(t, dir, "release.json")
		c := cfg
		c.Output = dir
		if e := Produce(context.Background(), c); e == nil {
			t.Fatal("existing output overwritten")
		}
		if !bytes.Equal(original, read(t, dir, "release.json")) {
			t.Fatal("output changed")
		}
		c.Output = filepath.Join(t.TempDir(), "cancelled")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if e := Produce(ctx, c); e == nil {
			t.Fatal("cancelled production succeeded")
		}
		if _, e := os.Stat(c.Output); !os.IsNotExist(e) {
			t.Fatal("cancelled output exists")
		}
	})
}

type testEntry struct {
	name string
	mode os.FileMode
	data []byte
}

func entries(t *testing.T, platform string, body []byte) []testEntry {
	t.Helper()
	var result []testEntry
	if platform == "linux" {
		g, e := gzip.NewReader(bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		defer g.Close()
		r := tar.NewReader(g)
		for {
			h, e := r.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			if e != nil {
				t.Fatal(e)
			}
			result = append(result, testEntry{h.Name, h.FileInfo().Mode(), data})
		}
	} else {
		r, e := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range r.File {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, e := io.ReadAll(r)
			if e != nil {
				t.Fatal(e)
			}
			if e = r.Close(); e != nil {
				t.Fatal(e)
			}
			result = append(result, testEntry{f.Name, f.Mode(), data})
		}
	}
	return result
}
func testArchive(t *testing.T, platform string, entries []testEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	if platform == "linux" {
		g := gzip.NewWriter(&b)
		w := tar.NewWriter(g)
		for _, entry := range entries {
			kind := byte(tar.TypeReg)
			size := int64(len(entry.data))
			link := ""
			if entry.mode&os.ModeSymlink != 0 {
				kind = tar.TypeSymlink
				size = 0
				link = "/escape"
			}
			if e := w.WriteHeader(&tar.Header{Name: entry.name, Typeflag: kind, Mode: int64(entry.mode.Perm()), Size: size, Linkname: link, Format: tar.FormatUSTAR}); e != nil {
				t.Fatal(e)
			}
			if kind == tar.TypeReg {
				if _, e := w.Write(entry.data); e != nil {
					t.Fatal(e)
				}
			}
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
		if e := g.Close(); e != nil {
			t.Fatal(e)
		}
	} else {
		w := zip.NewWriter(&b)
		for _, entry := range entries {
			h := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
			h.SetMode(entry.mode)
			f, e := w.CreateHeader(h)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.Write(entry.data); e != nil {
				t.Fatal(e)
			}
		}
		if e := w.Close(); e != nil {
			t.Fatal(e)
		}
	}
	return b.Bytes()
}
func addArchiveMember(t *testing.T, platform string, original []byte, name string) []byte {
	t.Helper()
	all := entries(t, platform, original)
	all = append(all, testEntry{name, 0644, []byte("unexpected")})
	return testArchive(t, platform, all)
}
