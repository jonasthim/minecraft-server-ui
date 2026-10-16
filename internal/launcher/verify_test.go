package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// This is a tiny local integrity fixture, not a downloaded/qualified JVM.
func runtimeFixture(t *testing.T) (Config, domain.LaunchSpec) {
	t.Helper()
	data := t.TempDir()
	archiveHash := strings.Repeat("a", 64)
	id := "temurin-17-" + runtime.GOOS + "-x64-" + archiveHash
	bin := "java"
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	executable := filepath.Join(data, "runtimes", id, "bin", bin)
	if err := os.MkdirAll(filepath.Dir(executable), 0700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("runtime integrity fixture; never executed")
	if err := os.WriteFile(executable, contents, 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	verified := domain.JavaRuntime{ID: id, Vendor: "Eclipse Adoptium", Major: 17, Version: "17.0.20+1", OS: runtime.GOOS, Architecture: "x64", Executable: executable, Artifact: domain.Artifact{SourceURL: "https://api.adoptium.net/v3/assets/version/17", Checksum: domain.Checksum{Algorithm: "sha256", Value: archiveHash}}}
	record := map[string]any{"schema": 1, "runtime": verified, "verified_sha256": archiveHash, "verified_at": time.Now().UTC(), "files": map[string]any{"bin/" + bin: map[string]any{"sha256": hex.EncodeToString(sum[:]), "size": len(contents), "executable": info.Mode()&0111 != 0}}}
	if err = WriteJSON(filepath.Join(data, "runtimes", id, ".runtime.json"), record); err != nil {
		t.Fatal(err)
	}
	return Config{DataDir: data}, domain.LaunchSpec{Executable: executable, JavaRuntimeID: id, JavaChecksum: verified.Artifact.Checksum}
}
func TestManagedRuntimeGateRejectsChangedRuntimeAndMismatchedIdentity(t *testing.T) {
	for _, kind := range []string{"valid", "checksum", "executable", "tampered", "missing-id"} {
		t.Run(kind, func(t *testing.T) {
			cfg, spec := runtimeFixture(t)
			switch kind {
			case "checksum":
				spec.JavaChecksum.Value = strings.Repeat("b", 64)
			case "executable":
				spec.Executable = filepath.Join(cfg.DataDir, "external-java")
			case "tampered":
				if err := os.WriteFile(spec.Executable, []byte("tampered"), 0755); err != nil {
					t.Fatal(err)
				}
			case "missing-id":
				spec.JavaRuntimeID = ""
			}
			err := verifyManagedRuntime(context.Background(), cfg, spec)
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && err == nil {
				t.Fatalf("managed runtime gate accepted %s", kind)
			}
		})
	}
}

func TestSealRejectsInvalidSpecWithoutReplacingActivation(t *testing.T) {
	cfg, spec := runtimeFixture(t)
	spec.WorkingDir = filepath.Join(cfg.DataDir, "instances", "test", "server")
	spec.Args = []string{"-jar", "server.jar", "nogui"}
	if err := os.MkdirAll(spec.WorkingDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteSpec(cfg.DataDir, "test", spec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cfg.DataDir, "instances", "test", "state", "launch.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	spec.JavaChecksum.Value = strings.Repeat("b", 64)
	if err = Seal(context.Background(), cfg.DataDir, "test", spec); err == nil {
		t.Fatal("invalid runtime activation accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid activation destroyed previous specification")
	}
}

func TestEnvironmentRemovesInheritedJavaAndNativeLoaders(t *testing.T) {
	for _, key := range []string{"JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH", "LD_PRELOAD", "LD_AUDIT", "LD_LIBRARY_PATH", "GCONV_PATH"} {
		t.Setenv(key, "injection")
	}
	t.Setenv("MINECRAFT_NATIVE_TEST_PRESERVE", "yes")
	got := strings.Join(environment(nil), "\n")
	if strings.Contains(got, "injection") {
		t.Fatal("inherited code loading override retained")
	}
	if !strings.Contains(got, "MINECRAFT_NATIVE_TEST_PRESERVE=yes") {
		t.Fatal("ordinary environment was discarded")
	}
}

func TestRunAdmissionConflictDoesNotCreateInstanceRoot(t *testing.T) {
	data := t.TempDir()
	release, err := AcquireAdmission(data, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err = Run(context.Background(), Config{DataDir: data, InstanceID: "test"}, nil); err == nil {
		t.Fatal("launcher entered a protected mutation")
	}
	if _, err = os.Stat(filepath.Join(data, "instances", "test")); !os.IsNotExist(err) {
		t.Fatalf("rejected launcher created ghost instance tree: %v", err)
	}
}
