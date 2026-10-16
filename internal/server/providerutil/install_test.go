package providerutil

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jar(t *testing.T, unsafe bool) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, e := z.Create("META-INF/MANIFEST.MF")
	if e != nil {
		t.Fatal(e)
	}
	_, _ = io.WriteString(w, "Manifest-Version: 1.0\r\nMain-Class: net.minecraft.server.Main\r\n\r\n")
	if unsafe {
		w, _ = z.Create("../world/level.dat")
		_, _ = io.WriteString(w, "unsafe")
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func setup(t *testing.T, data []byte) (*Installer, domain.InstallPlan, domain.JavaRuntime) {
	t.Helper()
	h := sha256.Sum256(data)
	hash := hex.EncodeToString(h[:])
	root := t.TempDir()
	if e := os.MkdirAll(filepath.Join(root, "instances", "test", "server"), 0700); e != nil {
		t.Fatal(e)
	}
	c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}, nil
	})}, Paper)
	i := NewInstaller(root, c)
	r := domain.JavaRuntime{ID: "fixture-java", Major: 21, Executable: filepath.Join(root, "runtimes", "fixture-java", "bin", "java"), Artifact: domain.Artifact{Checksum: domain.Checksum{Algorithm: "sha256", Value: strings.Repeat("1", 64)}}}
	i.getRuntime = func(context.Context, string) (domain.JavaRuntime, error) { return r, nil }
	plan := domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.4", LoaderVersion: "232"}, Requirements: domain.LaunchRequirements{JavaMajor: 21, MinJavaMajor: 21, MaxJavaMajor: 21}, Artifacts: []domain.Artifact{{ID: "paper/1.21.4/232", Provider: "paper", VersionID: "1.21.4/232", Filename: "paper-1.21.4-232.jar", Size: int64(len(data)), SourceURL: "https://fill-data.papermc.io/v1/objects/" + hash + "/paper-1.21.4-232.jar", Checksum: domain.Checksum{Algorithm: "sha256", Value: hash}}}}
	return i, plan, r
}
func TestPublishPreservesWorldAndLaunchesActualManagedJava(t *testing.T) {
	i, p, r := setup(t, jar(t, false))
	server := filepath.Join(i.dataDir, "instances", "test", "server")
	for _, path := range []string{"world/level.dat", "server.properties", "mods/local.jar", "plugins/config.yml", "eula.txt"} {
		full := filepath.Join(server, path)
		if e := os.MkdirAll(filepath.Dir(full), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(full, []byte("keep"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	spec, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if spec.Executable != r.Executable || spec.WorkingDir != server || spec.JavaRuntimeID != r.ID || spec.JavaChecksum != r.Artifact.Checksum || len(spec.Args) != 3 || spec.Args[0] != "-jar" || spec.Args[2] != "nogui" {
		t.Fatalf("invalid argv %+v", spec)
	}
	for _, path := range []string{"world/level.dat", "server.properties", "mods/local.jar", "plugins/config.yml", "eula.txt"} {
		b, e := os.ReadFile(filepath.Join(server, path))
		if e != nil || string(b) != "keep" {
			t.Fatalf("changed %s %q %v", path, b, e)
		}
	}
	b, e := os.ReadFile(spec.Args[1])
	if e != nil || !bytes.Equal(b, jar(t, false)) {
		t.Fatal("published bytes differ", e)
	}
	if _, e = os.Stat(filepath.Join(i.dataDir, "instances", "test", "state", "launch.json")); !os.IsNotExist(e) {
		t.Fatal("provider wrote supervisor launch state")
	}
	// A fresh manager reuses a verified immutable release despite a fetch timestamp change.
	p.Artifacts[0].RetrievedAt = p.Artifacts[0].RetrievedAt.AddDate(1, 0, 0)
	next, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard)
	if e != nil || next.Args[1] != spec.Args[1] {
		t.Fatalf("non deterministic/reuse error %+v %v", next, e)
	}
	manifest, e := os.ReadFile(filepath.Join(filepath.Dir(spec.Args[1]), "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var record map[string]any
	if e = json.Unmarshal(manifest, &record); e != nil || record["verified_sha256"] == nil {
		t.Fatal("missing verification provenance")
	}
}
func TestFailureLeavesPreviousExecutableAndWorld(t *testing.T) {
	for _, mode := range []string{"hash", "size", "interrupted", "cancelled", "unsafe jar", "invalid jar", "runtime mismatch", "unsafe id", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			i, p, r := setup(t, jar(t, false))
			old, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard)
			if e != nil {
				t.Fatal(e)
			}
			before, e := os.ReadFile(old.Args[1])
			if e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			id := "test"
			switch mode {
			case "hash":
				p.Artifacts[0].Checksum.Value = strings.Repeat("0", 64)
				p.Artifacts[0].SourceURL = "https://fill-data.papermc.io/v1/objects/" + strings.Repeat("0", 64) + "/paper-1.21.4-232.jar"
			case "size":
				p.Artifacts[0].Size++
			case "interrupted":
				p.Selection.LoaderVersion = "233"
				p.Artifacts[0].ID = "other"
				i.client.http.Transport = transport(func(*http.Request) (*http.Response, error) {
					return nil, errors.New("https://token:secret@evil.invalid")
				})
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unsafe jar":
				i, p, r = setup(t, jar(t, true))
			case "invalid jar":
				i, p, r = setup(t, []byte("not a zip"))
			case "runtime mismatch":
				r.Executable = "/tmp/unverified-java"
			case "unsafe id":
				id = "../../escape"
			case "symlink":
				if e = os.Rename(filepath.Dir(old.Args[1]), filepath.Dir(old.Args[1])+"-old"); e != nil {
					t.Fatal(e)
				}
				if e = os.Symlink(filepath.Dir(old.Args[1])+"-old", filepath.Dir(old.Args[1])); e != nil {
					t.Fatal(e)
				}
			}
			_, e = i.Publish(ctx, id, p, r, nil, io.Discard)
			if e == nil {
				t.Fatal("invalid install succeeded")
			}
			if strings.Contains(e.Error(), "secret") {
				t.Fatal("secret leak", e)
			}
			after, readErr := os.ReadFile(old.Args[1])
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("previous executable changed", readErr)
			}
		})
	}
}
func TestTamperedReleaseFailsRatherThanReplacing(t *testing.T) {
	i, p, r := setup(t, jar(t, false))
	s, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(s.Args[1], []byte("tampered"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = i.Publish(context.Background(), "test", p, r, nil, io.Discard); e == nil {
		t.Fatal("reused corrupt release")
	}
}
func TestURLPolicyAndMetadataBounds(t *testing.T) {
	for _, u := range []string{"http://fill.papermc.io/v3/projects/paper", "https://fill.papermc.io:443/v3/projects/paper", "https://user:secret@fill.papermc.io/v3/projects/paper", "https://fill.papermc.io/v3/projects/paper?token=secret", "https://fill.papermc.io.evil.invalid/v3/projects/paper", "https://fill-data.papermc.io/v1/objects/../../foo"} {
		if e := ValidateURL(Paper, u); e == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	for _, body := range []string{`{} {}`, strings.Repeat(" ", int(MaxMetadataBytes)+1)} {
		c := NewClient(&http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})}, Paper)
		var m map[string]any
		if _, e := c.Metadata(context.Background(), "https://fill.papermc.io/v3/projects/paper", &m); e == nil {
			t.Fatal("accepted malformed/oversized response")
		}
	}
}

type cancelReader struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r *cancelReader) Read(p []byte) (int, error) { n, e := r.reader.Read(p); r.cancel(); return n, e }
func TestCancellationAfterDownloadLeavesNoCandidateOrStage(t *testing.T) {
	data := jar(t, false)
	i, p, r := setup(t, data)
	old, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	world := filepath.Join(old.WorkingDir, "world", "level.dat")
	if e = os.MkdirAll(filepath.Dir(world), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(world, []byte("world-before"), 0600); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Selection.LoaderVersion = "233"
	i.client.http.Transport = transport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(&cancelReader{bytes.NewReader(data), cancel}), Header: make(http.Header)}, nil
	})
	_, e = i.Publish(ctx, "test", p, r, nil, io.Discard)
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("want cancelled, got %v", e)
	}
	entries, e := os.ReadDir(filepath.Dir(filepath.Dir(old.Args[1])))
	if e != nil || len(entries) != 1 {
		t.Fatalf("candidate or stage survived: %v %v", entries, e)
	}
	if _, e = os.Stat(old.Args[1]); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(world)
	if e != nil || string(b) != "world-before" {
		t.Fatal("world changed", e)
	}
}
func TestDownloadVerifiesPublishedDigestAndLocalSHA256(t *testing.T) {
	data := jar(t, false)
	i, p, _ := setup(t, data)
	var b bytes.Buffer
	sum, e := i.client.Download(context.Background(), p.Artifacts[0], &b)
	if e != nil || sum != p.Artifacts[0].Checksum.Value || !bytes.Equal(data, b.Bytes()) {
		t.Fatalf("verification %s %v", sum, e)
	}
	p.Artifacts[0].Checksum.Value = strings.Repeat("0", 64)
	p.Artifacts[0].SourceURL = "https://fill-data.papermc.io/v1/objects/" + strings.Repeat("0", 64) + "/paper-1.21.4-232.jar"
	if _, e = i.client.Download(context.Background(), p.Artifacts[0], io.Discard); e == nil {
		t.Fatal("accepted wrong published digest")
	}
}

func TestMissingInstanceCannotBeProvisionedByProvider(t *testing.T) {
	i, p, r := setup(t, jar(t, false))
	idRoot := filepath.Join(i.dataDir, "instances", "test")
	if e := os.RemoveAll(idRoot); e != nil {
		t.Fatal(e)
	}
	if _, e := i.Publish(context.Background(), "test", p, r, nil, io.Discard); e == nil {
		t.Fatal("provider created an instance without coordinator provisioning")
	}
	if _, e := os.Stat(idRoot); !os.IsNotExist(e) {
		t.Fatalf("provider mutated unprovisioned instance: %v", e)
	}
}

func TestRedirectRefusalDoesNotLeakOrContactTarget(t *testing.T) {
	requests := 0
	c := NewClient(&http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://user:secret@evil.invalid/private"}}, Request: r}, nil
	})}, Paper)
	var result map[string]any
	if _, e := c.Metadata(context.Background(), "https://fill.papermc.io/v3/projects/paper", &result); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatalf("redirect or secret escaped: %v", e)
	}
	if requests != 1 {
		t.Fatalf("redirect followed, %d requests", requests)
	}
}
