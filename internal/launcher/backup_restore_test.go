package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupRestoreMarkerBlocksIndependentLaunch(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "instances/test/state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkBackupRestoreReady(context.Background(), dir, "test"); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "backup-restore.json")
	for _, data := range []string{"", "garbage", "{\"operation_id\":\"123\"}"} {
		if err := os.WriteFile(marker, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkBackupRestoreReady(context.Background(), dir, "test"); err == nil {
			t.Fatal("pending restore admitted launch")
		}
	}
	os.Remove(marker)
	outside := t.TempDir()
	if err := os.Symlink(outside, marker); err != nil {
		t.Fatal(err)
	}
	if err := checkBackupRestoreReady(context.Background(), dir, "test"); err == nil {
		t.Fatal("unsafe marker admitted launch")
	}
}
