package launchargs

import (
	"reflect"
	"testing"
)

func TestStripMemoryAcceptsOnlyCanonicalPolicy(t *testing.T) {
	base := []string{"-jar", "/instance/server.jar", "nogui"}
	for _, prefix := range [][]string{nil, {"-Xms256M", "-Xmx256M"}, {"-Xms512M", "-Xmx512M"}, {"-Xms512M", "-Xmx2048M"}, {"-Xms512M", "-Xmx1048576M"}} {
		args := append(append([]string{}, prefix...), base...)
		actual, err := StripMemory(args)
		if err != nil || !reflect.DeepEqual(actual, base) {
			t.Fatal(args, actual, err)
		}
	}
	for _, args := range [][]string{{"-Xms512M"}, {"-Xmx2048M", "-Xms512M"}, {"-Xms0512M", "-Xmx2048M"}, {"-Xms512M", "-Xmx2G"}, {"-Xms256M", "-Xmx2048M"}, {"-Xms512M", "-Xmx255M"}, {"-Xms512M", "-Xmx1048577M"}, {"-Xms512M", "-Xmx2048M", "-Xmx4096M"}, {"-jar", "server.jar", "-Xmx2048M"}} {
		if _, err := StripMemory(args); err == nil {
			t.Fatalf("unsafe/noncanonical memory argv accepted: %q", args)
		}
	}
}
