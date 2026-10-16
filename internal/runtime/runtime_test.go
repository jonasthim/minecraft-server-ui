package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixtureEntry struct {
	name, data, link string
	kind             byte
}

func archiveFixture(t *testing.T, format string, entries []fixtureEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	if format == "zip" {
		z := zip.NewWriter(&b)
		for _, e := range entries {
			h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
			h.SetMode(0755)
			if e.kind == tar.TypeSymlink {
				h.SetMode(os.ModeSymlink | 0777)
			}
			w, err := z.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
		if err := z.Close(); err != nil {
			t.Fatal(err)
		}
	} else {
		g := gzip.NewWriter(&b)
		tr := tar.NewWriter(g)
		for _, e := range entries {
			kind := e.kind
			if kind == 0 {
				kind = tar.TypeReg
			}
			h := &tar.Header{Name: e.name, Mode: 0755, Typeflag: kind, Linkname: e.link}
			if kind == tar.TypeReg {
				h.Size = int64(len(e.data))
			}
			if err := tr.WriteHeader(h); err != nil {
				t.Fatal(err)
			}
			if kind == tar.TypeReg {
				if _, err := tr.Write([]byte(e.data)); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := tr.Close(); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return b.Bytes()
}
func runtimeArchive(t *testing.T) []byte {
	format, java := "tar.gz", "java"
	if hostOS() == "windows" {
		format, java = "zip", "java.exe"
	}
	return archiveFixture(t, format, []fixtureEntry{{name: "jdk-21/bin/" + java, data: "fake executable"}, {name: "jdk-21/release", data: "IMPLEMENTOR=\"Eclipse Adoptium\"\nJAVA_RUNTIME_VERSION=\"21.0.8+9-LTS\"\nOS_ARCH=\"x86_64\"\n"}})
}
func fixtureManager(t *testing.T, data []byte, hash string) (*Manager, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		var body []byte
		if r.URL.Host == "api.adoptium.net" {
			suffix := ".tar.gz"
			if hostOS() == "windows" {
				suffix = ".zip"
			}
			body = []byte(fmt.Sprintf(`[{"vendor":"eclipse","release_name":"jdk-21.0.8+9","version":{"major":21,"openjdk_version":"21.0.8+9-LTS"},"binary":{"architecture":"x64","os":%q,"image_type":"jdk","jvm_impl":"hotspot","heap_size":"normal","package":{"name":%q,"link":"https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21/test.tar.gz","checksum":%q,"size":%d}}}]`, hostOS(), "OpenJDK21U-jdk_x64_"+hostOS()+"_hotspot"+suffix, hash, len(data)))
		} else if r.URL.Host == "github.com" {
			calls.Add(1)
			body = data
		} else {
			return nil, fmt.Errorf("unexpected URL %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}
	m := New(t.TempDir(), client)
	m.probe = func(context.Context, string) (jvmInfo, error) {
		return jvmInfo{Vendor: "Eclipse Adoptium", Version: "21.0.8+9-LTS", Architecture: "amd64", Major: 21}, nil
	}
	return m, calls
}
func sha(data []byte) string { d := sha256.Sum256(data); return hex.EncodeToString(d[:]) }

// Dropping digest verification or publishing before probe would expose a record here.
func TestEnsureRejectsBadHashWithoutActivation(t *testing.T) {
	m, _ := fixtureManager(t, runtimeArchive(t), strings.Repeat("0", 64))
	if _, err := m.Ensure(context.Background(), 21, nil); err == nil {
		t.Fatal("accepted bad hash")
	}
	list, err := m.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("activated bad runtime: %v %v", list, err)
	}
}
func TestEnsureInstallsAndReusesOfflineWithIntegrityChecks(t *testing.T) {
	data := runtimeArchive(t)
	m, calls := fixtureManager(t, data, sha(data))
	r, err := m.Ensure(context.Background(), 21, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Major != 21 || r.Executable == "" || !filepath.IsAbs(r.Executable) {
		t.Fatalf("invalid runtime %+v", r)
	}
	got, err := New(m.dataDir, m.client).Get(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != r.ID {
		t.Fatal("changed runtime id")
	}
	r2, err := m.Ensure(context.Background(), 21, nil)
	if err != nil || r2.ID != r.ID || calls.Load() != 1 {
		t.Fatalf("not reused: %v %v downloads=%d", r2, err, calls.Load())
	}
	if err := os.WriteFile(r.Executable, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Get(context.Background(), r.ID); err == nil {
		t.Fatal("accepted modified executable")
	}
}
func TestConcurrentManagersShareInstall(t *testing.T) {
	data := runtimeArchive(t)
	m, calls := fixtureManager(t, data, sha(data))
	var wg sync.WaitGroup
	errch := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other := New(m.dataDir, m.client)
			other.probe = m.probe
			_, err := other.Ensure(context.Background(), 21, nil)
			errch <- err
		}()
	}
	wg.Wait()
	close(errch)
	for err := range errch {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent downloads=%d", calls.Load())
	}
}
func TestProbeFailureNeverActivates(t *testing.T) {
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	m.probe = func(context.Context, string) (jvmInfo, error) {
		return jvmInfo{Major: 21, Vendor: "Other", Architecture: "amd64", Version: "21.0.8+9-LTS"}, nil
	}
	if _, err := m.Ensure(context.Background(), 21, nil); err == nil {
		t.Fatal("accepted non-Temurin runtime")
	}
	got, err := m.List(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("published failed probe: %v %v", got, err)
	}
}
func TestCancelledDownloadLeavesExistingRuntimeIntact(t *testing.T) {
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	installed, err := m.Ensure(context.Background(), 21, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Ensure(ctx, 17, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation: %v", err)
	}
	if _, err := m.Get(context.Background(), installed.ID); err != nil {
		t.Fatalf("existing installation changed: %v", err)
	}
}
func TestArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		for _, name := range []string{"../escape", "/root/escape", "C:/escape", "jdk\\..\\escape", "jdk/a:stream", "jdk/CON.txt", "jdk/aux", "jdk/trailing.", "jdk/trailing ", "jdk/a/../../escape", "jdk/" + strings.Repeat("a", 256)} {
			t.Run(format+"/"+name, func(t *testing.T) {
				data := archiveFixture(t, format, []fixtureEntry{{name: name, data: "unsafe"}})
				p := filepath.Join(t.TempDir(), "archive")
				if err := os.WriteFile(p, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := extractArchive(context.Background(), p, format, t.TempDir()); err == nil {
					t.Fatal("accepted unsafe archive path")
				}
			})
		}
	}
}
func TestArchiveRejectsDuplicateAndCaseCollision(t *testing.T) {
	for _, format := range []string{"tar.gz", "zip"} {
		for _, second := range []string{"jdk/bin/java", "JDK/bin/Java"} {
			t.Run(format+second, func(t *testing.T) {
				data := archiveFixture(t, format, []fixtureEntry{{name: "jdk/bin/java", data: "first"}, {name: second, data: "second"}})
				p := filepath.Join(t.TempDir(), "archive")
				_ = os.WriteFile(p, data, 0600)
				if err := extractArchive(context.Background(), p, format, t.TempDir()); err == nil {
					t.Fatal("accepted colliding archive entries")
				}
			})
		}
	}
}
func TestArchiveRejectsEscapingLinksAndMaterializesInternalLinks(t *testing.T) {
	for _, target := range []string{"../../outside", "/etc/passwd", "C:/Windows/evil"} {
		data := archiveFixture(t, "tar.gz", []fixtureEntry{{name: "jdk/legal/file", kind: tar.TypeSymlink, link: target}})
		p := filepath.Join(t.TempDir(), "a")
		_ = os.WriteFile(p, data, 0600)
		if err := extractArchive(context.Background(), p, "tar.gz", t.TempDir()); err == nil {
			t.Fatalf("accepted escaping link %s", target)
		}
	}
	data := archiveFixture(t, "tar.gz", []fixtureEntry{{name: "jdk/legal/a", data: "license"}, {name: "jdk/legal/b", kind: tar.TypeSymlink, link: "a"}})
	p := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(p, data, 0600)
	dest := t.TempDir()
	if err := extractArchive(context.Background(), p, "tar.gz", dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dest, "jdk/legal/b"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("link not materialized: %v %v", info, err)
	}
}
func TestCorruptArchiveAndCancelledExtraction(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a")
	_ = os.WriteFile(p, []byte("broken"), 0600)
	for _, format := range []string{"zip", "tar.gz"} {
		if err := extractArchive(context.Background(), p, format, t.TempDir()); err == nil {
			t.Fatal("accepted corrupt archive")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := extractArchive(ctx, p, "tar.gz", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation: %v", err)
	}
}
func TestPublisherURLPolicy(t *testing.T) {
	for _, raw := range []string{"http://api.adoptium.net/v3/assets", "https://api.adoptium.net.evil/v3", "https://github.com/evil/payload", "https://github.com/adoptium/temurin21-binaries/../evil", "https://user:pass@api.adoptium.net/v3/assets", "https://api.adoptium.net:444/v3/assets", "https://127.0.0.1/private"} {
		if allowedURL(raw, false) == nil {
			t.Errorf("accepted unsafe URL %s", raw)
		}
	}
	for _, raw := range []string{"https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21/a.tar.gz", "https://api.adoptium.net/v3/assets/latest/21/hotspot"} {
		if err := allowedURL(raw, false); err != nil {
			t.Errorf("official URL rejected: %v", err)
		}
	}
}
func TestParseJVMRejectsWrongMajorVendorArchitecture(t *testing.T) {
	output := "Property settings:\n    java.vendor = Eclipse Adoptium\n    java.runtime.version = 21.0.8+9-LTS\n    java.version = 21.0.8\n    os.arch = amd64\n"
	got, err := parseJVM(output)
	if err != nil || got.Major != 21 || got.Version != "21.0.8+9-LTS" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := parseJVM(strings.ReplaceAll(output, "amd64", "aarch64")); err == nil {
		t.Fatal("accepted wrong architecture")
	}
	if _, err := parseJVM(strings.ReplaceAll(output, "Eclipse Adoptium", "Oracle")); err == nil {
		t.Fatal("accepted wrong vendor")
	}
}
func TestSelectRequirementsAndRejectUnknownTuples(t *testing.T) {
	for _, tc := range []struct {
		f    domain.Flavor
		v    string
		java int
	}{{domain.FlavorVanilla, "1.12.2", 8}, {domain.FlavorVanilla, "1.16.5", 8}, {domain.FlavorVanilla, "1.17.1", 16}, {domain.FlavorVanilla, "1.18.2", 17}, {domain.FlavorVanilla, "1.20.4", 17}, {domain.FlavorVanilla, "1.20.5", 21}, {domain.FlavorVanilla, "1.21.9", 21}, {domain.FlavorVanilla, "26.1", 25}, {domain.FlavorPaper, "1.16.5", 16}, {domain.FlavorPaper, "1.20.1", 21}, {domain.FlavorFabric, "1.17.1", 17}, {domain.FlavorForge, "1.16.5", 8}, {domain.FlavorNeoForge, "1.20.2", 17}} {
		r, err := Select(domain.VersionSelection{Flavor: tc.f, MinecraftVersion: tc.v})
		if err != nil || r.JavaMajor != tc.java {
			t.Errorf("%s %s: %+v %v", tc.f, tc.v, r, err)
		}
	}
	for _, sel := range []domain.VersionSelection{{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.12.1"}, {Flavor: domain.FlavorFabric, MinecraftVersion: "1.13.2"}, {Flavor: domain.FlavorNeoForge, MinecraftVersion: "1.20.1"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "27.1"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.22"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "26.4"}, {Flavor: "evil", MinecraftVersion: "1.21"}, {Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21-pre1"}} {
		if _, err := Select(sel); err == nil {
			t.Errorf("accepted unsupported %+v", sel)
		}
	}
}
func TestDownloadCancellationDuringRead(t *testing.T) {
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	ctx, cancel := context.WithCancel(context.Background())
	prior := m.client.Transport
	m.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		resp, err := prior.RoundTrip(req)
		if err == nil && req.URL.Host == "github.com" {
			resp.Body = &cancelReader{cancel: cancel}
		}
		return resp, err
	})
	if _, err := m.Ensure(ctx, 21, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want interrupted download error: %v", err)
	}
	list, err := m.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("interrupted runtime published: %v %v", list, err)
	}
}

type cancelReader struct{ cancel context.CancelFunc }

func (c *cancelReader) Read([]byte) (int, error) { c.cancel(); return 0, context.Canceled }
func (c *cancelReader) Close() error             { return nil }
func TestManifestRejectsUnsafeID(t *testing.T) {
	m := New(t.TempDir(), nil)
	if _, err := m.Get(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe id accepted")
	}
}

func TestReadsRejectSymlinkedRuntimeRoot(t *testing.T) {
	if hostOS() == "windows" {
		t.Skip("Windows symlink creation requires host privileges; native qualification remains required")
	}
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	r, err := m.Ensure(context.Background(), 21, nil)
	if err != nil {
		t.Fatal(err)
	}
	other := New(t.TempDir(), nil)
	if err := os.Symlink(m.root(), other.root()); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(m.root(), r.ID, ".runtime.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var record manifest
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record.Runtime.Executable = filepath.Join(other.root(), r.ID, "bin", "java")
	raw, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := other.Get(context.Background(), r.ID); err == nil {
		t.Error("Get followed a symlinked managed root")
	}
	if _, err := other.List(context.Background()); err == nil {
		t.Error("List followed a symlinked managed root")
	}
}
func TestWindowsZipExtractsWithoutGlobalInstallation(t *testing.T) {
	data := archiveFixture(t, "zip", []fixtureEntry{{name: "jdk-21/bin/java.exe", data: "windows executable fixture"}, {name: "jdk-21/release", data: "release fixture"}})
	p := filepath.Join(t.TempDir(), "windows.zip")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := extractArchive(context.Background(), p, "zip", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "jdk-21/bin/java.exe"))
	if err != nil || string(got) != "windows executable fixture" {
		t.Fatalf("zip layout lost: %q %v", got, err)
	}
}
func TestArchiveRejectsOversizedAndSpecialEntries(t *testing.T) {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	tr := tar.NewWriter(g)
	if err := tr.WriteHeader(&tar.Header{Name: "jdk/bomb", Mode: 0600, Size: 1<<30 + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()
	_ = g.Close()
	p := filepath.Join(t.TempDir(), "oversized.tar.gz")
	if err := os.WriteFile(p, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), p, "tar.gz", t.TempDir()); err == nil {
		t.Fatal("oversized entry accepted")
	}
	for _, kind := range []byte{tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
		p := filepath.Join(t.TempDir(), "special")
		if err := os.WriteFile(p, archiveFixture(t, "tar.gz", []fixtureEntry{{name: "jdk/special", kind: kind}}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(context.Background(), p, "tar.gz", t.TempDir()); err == nil {
			t.Fatal("special entry accepted")
		}
	}
}
func TestMetadataRejectsForeignArtifactBeforeDownload(t *testing.T) {
	data := runtimeArchive(t)
	for _, mutation := range []func(map[string]any){
		func(v map[string]any) { v["vendor"] = "other" },
		func(v map[string]any) { v["version"].(map[string]any)["major"] = 17 },
		func(v map[string]any) { v["binary"].(map[string]any)["architecture"] = "aarch64" },
		func(v map[string]any) {
			v["binary"].(map[string]any)["package"].(map[string]any)["name"] = "../payload.tar.gz"
		},
		func(v map[string]any) {
			v["binary"].(map[string]any)["package"].(map[string]any)["link"] = "https://evil.example/payload.tar.gz"
		},
		func(v map[string]any) {
			v["binary"].(map[string]any)["package"].(map[string]any)["link"] = "https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17/payload.tar.gz"
		},
		func(v map[string]any) { v["binary"].(map[string]any)["package"].(map[string]any)["checksum"] = "abc" },
	} {
		m, calls := fixtureManager(t, data, sha(data))
		prior := m.client.Transport
		m.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := prior.RoundTrip(req)
			if err != nil || req.URL.Host != "api.adoptium.net" {
				return resp, err
			}
			var entries []map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			mutation(entries[0])
			body, err := json.Marshal(entries)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			return resp, nil
		})
		if _, err := m.Ensure(context.Background(), 21, nil); err == nil {
			t.Error("foreign metadata accepted")
		}
		if calls.Load() != 0 {
			t.Error("downloaded artifact before metadata validation")
		}
	}
}
func TestDownloadRejectsRedirectOutsidePublisher(t *testing.T) {
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	prior := m.client.Transport
	m.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "github.com" {
			return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://evil.example/payload"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		if req.URL.Host != "api.adoptium.net" {
			t.Fatal("requested external redirect host")
		}
		return prior.RoundTrip(req)
	})
	if _, err := m.Ensure(context.Background(), 21, nil); err == nil {
		t.Fatal("external publisher redirect accepted")
	}
}
func TestEnvironmentStripsJavaInjectionVariables(t *testing.T) {
	for _, key := range []string{"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "JAVA_HOME", "CLASSPATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES"} {
		t.Setenv(key, "do not run")
	}
	for _, entry := range javaEnvironment() {
		if strings.HasSuffix(entry, "=do not run") {
			t.Fatalf("unsafe inherited JVM environment: %s", entry)
		}
	}
}
func TestLegacyTemurinVersionProbe(t *testing.T) {
	got, err := parseJVM("    java.vendor = Eclipse Adoptium\n    java.runtime.version = 1.8.0_462-b08\n    java.version = 1.8.0_462\n    os.arch = amd64\n")
	if err != nil || got.Major != 8 {
		t.Fatalf("legacy Java 8 rejected: %+v %v", got, err)
	}
}
func TestWaitingInstallerIsCancellable(t *testing.T) {
	data := runtimeArchive(t)
	m, _ := fixtureManager(t, data, sha(data))
	release, err := lock(context.Background(), m.root())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := m.Ensure(ctx, 21, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting installer ignored context: %v", err)
	}
}

func TestArchiveRejectsDataBearingDirectoryHeader(t *testing.T) {
	var b bytes.Buffer
	g := gzip.NewWriter(&b)
	tr := tar.NewWriter(g)
	if err := tr.WriteHeader(&tar.Header{Name: "jdk/", Mode: 0700, Typeflag: tar.TypeDir, Size: 1<<30 + 1}); err != nil {
		t.Fatal(err)
	}
	_ = tr.Close()
	_ = g.Close()
	p := filepath.Join(t.TempDir(), "dir.tar.gz")
	if err := os.WriteFile(p, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(context.Background(), p, "tar.gz", t.TempDir()); err == nil {
		t.Fatal("accepted nonzero directory payload")
	}
}
