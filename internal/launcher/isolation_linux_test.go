//go:build linux

package launcher

import (
	"strings"
	"testing"
)

func TestPrivateIdentityMapRejectsHostOrExtraMappings(t *testing.T) {
	for name, tc := range map[string]struct {
		raw string
		ok  bool
	}{
		"minimal":      {"0 0 1\n997 997 1\n", true},
		"host":         {"0 0 4294967295\n", false},
		"extra":        {"0 0 1\n997 997 1\n998 998 1\n", false},
		"remapped":     {"0 0 1\n997 123 1\n", false},
		"missing root": {"997 997 1\n", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := validatePrivateMap(tc.raw, 997); (got == nil) != tc.ok {
				t.Fatalf("error=%v", got)
			}
		})
	}
}
func TestMountIsolationRequiresPinnedParentsAndImmutableArtifacts(t *testing.T) {
	root := "/var/lib/minecraft-ui/instances/test"
	paths := map[string]string{"/var/lib/minecraft-ui": "ro", "/var/lib/minecraft-ui/runtimes": "ro", root: "ro", root + "/server": "rw", root + "/logs": "rw", root + "/state": "rw", root + "/server/.manager": "ro", root + "/state/launch.json": "ro", "/var/lib/minecraft-ui/locks/test.lock": "rw"}
	render := func() string {
		var out strings.Builder
		for p, mode := range paths {
			out.WriteString("1 0 0:1 / " + p + " " + mode + ",nosuid - tmpfs tmpfs rw\n")
		}
		return out.String()
	}
	if err := validateIsolationMounts(render(), "test"); err != nil {
		t.Fatal(err)
	}
	for path, mode := range paths {
		t.Run(path, func(t *testing.T) {
			paths[path] = "rw"
			if mode == "rw" {
				paths[path] = "ro"
			}
			if err := validateIsolationMounts(render(), "test"); err == nil {
				t.Fatal("unsafe mount accepted")
			}
			paths[path] = mode
		})
	}
}

func TestMountIsolationRequiresHiddenDataRoot(t *testing.T) {
	if err := validateIsolationMounts("1 0 0:1 / /var/lib/minecraft-ui ro - ext4 /dev/sda rw\n", "test"); err == nil {
		t.Fatal("ordinary host filesystem accepted as hidden data root")
	}
}
