package candidate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeSnapshotRejectsUnapprovedContractsBeforeIO(t *testing.T) {
	for _, test := range []struct {
		platform, command, version string
		schema                     int64
	}{
		{"darwin", "minecraft-ui-bootstrap", "v1.2.3", 0},
		{"linux", "unknown", "v1.2.3", 0},
		{"linux", "minecraft-ui-bootstrap", "latest", 0},
		{"linux", "minecraft-ui-bootstrap", "v1.2.3", 1},
		{"windows", "minecraft-ui-update", "v1.2.3", 1},
		{"linux", "minecraft-ui", "v1.2.3", 0},
	} {
		t.Run(test.platform+"/"+test.command+"/"+test.version, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "snapshot")
			if _, e := SnapshotNativeInput(context.Background(), "missing", destination, test.platform, test.command, test.version, test.schema); e == nil {
				t.Fatal("accepted unapproved snapshot contract")
			}
			if _, e := os.Lstat(destination); !os.IsNotExist(e) {
				t.Fatal("created snapshot before validation", e)
			}
		})
	}
}
