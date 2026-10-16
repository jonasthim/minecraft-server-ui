package runtime

import (
	"context"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

// Current official Temurin 8 reports a JVM vendor different from its release
// implementor. Accepting all JVM vendors or other builds must fail this table.
func TestJava8TemurinJVMVendor(t *testing.T) {
	for _, tc := range []struct {
		name, vendor, version string
		major                 int
		valid                 bool
	}{
		{"official", "Temurin", "1.8.0_504-b01", 8, true},
		{"wrongVendor", "Oracle", "1.8.0_504-b01", 8, false},
		{"differentBuild", "Temurin", "1.8.0_504-b02", 8, false},
		{"differentMajor", "Temurin", "11.0.1+1", 11, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := "1.8.0_504"
			if tc.major != 8 {
				version = "11.0.1"
			}
			got, err := parseJVM(fmt.Sprintf("java.vendor = %s\njava.runtime.version = %s\njava.version = %s\nos.arch = amd64\n", tc.vendor, tc.version, version))
			if (err == nil) != tc.valid {
				t.Fatalf("parse validity=%t want %t: %v", err == nil, tc.valid, err)
			}
			if tc.valid && (got.Major != 8 || got.Version != "1.8.0_504-b01") {
				t.Fatalf("unexpected JVM: %+v", got)
			}
			m := New(t.TempDir(), nil)
			m.probe = func(context.Context, string) (jvmInfo, error) {
				return jvmInfo{Major: tc.major, Vendor: tc.vendor, Version: tc.version, Architecture: "amd64"}, nil
			}
			err = m.verifyProbe(context.Background(), domain.JavaRuntime{Major: tc.major, Version: tc.version})
			if (err == nil) != tc.valid {
				t.Fatalf("probe validity=%t want %t: %v", err == nil, tc.valid, err)
			}
		})
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("IMPLEMENTOR=\"Temurin\"\nJAVA_RUNTIME_VERSION=\"1.8.0_504-b01\"\nOS_ARCH=\"amd64\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateRelease(root, domain.JavaRuntime{Major: 8, Version: "1.8.0_504-b01"}); err == nil {
		t.Fatal("JVM vendor alias accepted as release implementor")
	}
	m := New(root, nil)
	m.probe = func(context.Context, string) (jvmInfo, error) {
		return jvmInfo{Major: 8, Vendor: "Temurin", Version: "1.8.0_504-b01", Architecture: "amd64"}, nil
	}
	if err := m.verifyProbe(context.Background(), domain.JavaRuntime{Major: 8, Version: "1.8.0_504-b02"}); err == nil {
		t.Fatal("accepted JVM build different from official metadata")
	}
}
