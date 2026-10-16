package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// The final official Temurin 16 build predates the Eclipse Adoptium vendor name.
// Each gate must accept that exact build and reject a wider vendor exception.
func TestHistoricalJava16VendorValidation(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, version, arch string
		major                       int
		valid                       bool
	}{
		{"official16", "Eclipse Foundation", "16.0.2+7", "amd64", 16, true},
		{"wrongVendor", "Oracle", "16.0.2+7", "amd64", 16, false},
		{"other16Build", "Eclipse Foundation", "16.0.2+8", "amd64", 16, false},
		{"otherMajor", "Eclipse Foundation", "17.0.1+12", "amd64", 17, false},
		{"wrongMajor", "Eclipse Foundation", "16.0.2+7", "amd64", 17, false},
		{"wrongArch", "Eclipse Foundation", "16.0.2+7", "aarch64", 16, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := fmt.Sprintf("java.vendor = %s\njava.runtime.version = %s\njava.version = %d.0.2\nos.arch = %s\n", tc.vendor, tc.version, tc.major, tc.arch)
			t.Run("parse", func(t *testing.T) {
				got, err := parseJVM(output)
				if (err == nil) != tc.valid {
					t.Fatalf("parse validity=%t, want %t: %v", err == nil, tc.valid, err)
				}
				if tc.valid && (got.Major != 16 || got.Version != "16.0.2+7") {
					t.Fatalf("unexpected JVM: %+v", got)
				}
			})
			t.Run("release", func(t *testing.T) {
				root := t.TempDir()
				release := fmt.Sprintf("IMPLEMENTOR=\"%s\"\nFULL_VERSION=\"%s\"\nOS_ARCH=\"%s\"\n", tc.vendor, tc.version, tc.arch)
				if err := os.WriteFile(filepath.Join(root, "release"), []byte(release), 0600); err != nil {
					t.Fatal(err)
				}
				err := validateRelease(root, domain.JavaRuntime{Major: tc.major, Version: tc.version})
				if (err == nil) != tc.valid {
					t.Fatalf("release validity=%t, want %t: %v", err == nil, tc.valid, err)
				}
			})
			t.Run("probe", func(t *testing.T) {
				m := New(t.TempDir(), nil)
				m.probe = func(context.Context, string) (jvmInfo, error) {
					return jvmInfo{Major: tc.major, Vendor: tc.vendor, Version: tc.version, Architecture: tc.arch}, nil
				}
				err := m.verifyProbe(context.Background(), domain.JavaRuntime{Major: tc.major, Version: tc.version})
				if (err == nil) != tc.valid {
					t.Fatalf("probe validity=%t, want %t: %v", err == nil, tc.valid, err)
				}
			})
		})
	}
}

func TestHistoricalJava16RequiresExactReleaseAndProbeBuild(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"", "16.0.2+8"} {
		release := fmt.Sprintf("IMPLEMENTOR=\"Eclipse Foundation\"\nJAVA_RUNTIME_VERSION=\"%s\"\nOS_ARCH=\"amd64\"\n", version)
		if err := os.WriteFile(filepath.Join(root, "release"), []byte(release), 0600); err != nil {
			t.Fatal(err)
		}
		if err := validateRelease(root, domain.JavaRuntime{Major: 16, Version: "16.0.2+7"}); err == nil {
			t.Fatalf("accepted release build %q", version)
		}
	}
	m := New(root, nil)
	m.probe = func(context.Context, string) (jvmInfo, error) {
		return jvmInfo{Major: 16, Vendor: "Eclipse Foundation", Version: "16.0.2+7", Architecture: "amd64"}, nil
	}
	if err := m.verifyProbe(context.Background(), domain.JavaRuntime{Major: 16, Version: "16.0.2+8"}); err == nil {
		t.Fatal("accepted probe for a different metadata build")
	}
}

func TestHistoricalJava16InstallsVerifiedAndReuses(t *testing.T) {
	format, java := "tar.gz", "java"
	if hostOS() == "windows" {
		format, java = "zip", "java.exe"
	}
	data := archiveFixture(t, format, []fixtureEntry{
		{name: "jdk-16.0.2+7/bin/" + java, data: "fixture executable"},
		{name: "jdk-16.0.2+7/release", data: "IMPLEMENTOR=\"Eclipse Foundation\"\nIMPLEMENTOR_VERSION=\"Temurin-16.0.2+7\"\nJAVA_VERSION=\"16.0.2\"\nFULL_VERSION=\"16.0.2+7\"\nOS_ARCH=\"x86_64\"\n"},
	})
	var meta metadata
	meta.Vendor, meta.ReleaseName = "eclipse", "jdk-16.0.2+7"
	meta.Version.Major, meta.Version.OpenJDKVersion = 16, "16.0.2+7"
	meta.Binary.Architecture, meta.Binary.OS = "x64", hostOS()
	meta.Binary.ImageType, meta.Binary.JVMImpl, meta.Binary.HeapSize = "jdk", "hotspot", "normal"
	meta.Binary.Package.Name = "OpenJDK16U-jdk_x64_" + hostOS() + "_hotspot_16.0.2_7." + format
	meta.Binary.Package.Link = "https://github.com/adoptium/temurin16-binaries/releases/download/jdk-16.0.2%2B7/" + meta.Binary.Package.Name
	meta.Binary.Package.Checksum, meta.Binary.Package.Size = sha(data), int64(len(data))
	raw, err := json.Marshal([]metadata{meta})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := raw
		if req.URL.Host == "github.com" {
			body = data
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: req}, nil
	})}
	m := New(t.TempDir(), client)
	m.probe = func(context.Context, string) (jvmInfo, error) {
		return parseJVM("java.vendor = Eclipse Foundation\njava.version = 16.0.2\njava.runtime.version = 16.0.2+7\nos.arch = amd64\n")
	}
	first, err := m.Ensure(context.Background(), 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Vendor != "Eclipse Adoptium" || first.Artifact.Checksum.Value != sha(data) {
		t.Fatalf("unverified or unnormalized runtime: %+v", first)
	}
	fresh := New(m.dataDir, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("reuse made a network request")
		return nil, fmt.Errorf("offline")
	})})
	fresh.probe = m.probe
	for _, get := range []func() (domain.JavaRuntime, error){
		func() (domain.JavaRuntime, error) { return fresh.Get(context.Background(), first.ID) },
		func() (domain.JavaRuntime, error) { return fresh.Ensure(context.Background(), 16, nil) },
	} {
		got, err := get()
		if err != nil || got.ID != first.ID || got.Executable != first.Executable {
			t.Fatalf("fresh-manager reuse failed: %+v %v", got, err)
		}
	}
}
