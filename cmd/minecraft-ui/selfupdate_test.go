package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"os"
	"path/filepath"
	"testing"
)

func TestSelfUpdateFixedReadOnlyProbes(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var out bytes.Buffer
	handled, err := runSelfUpdateProbe(ctx, []string{"update-capability"}, &out)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	var cap selfupdate.Capability
	if json.Unmarshal(out.Bytes(), &cap) != nil || cap.Schema != db.LatestVersion() {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "manager.db")); !os.IsNotExist(err) {
		t.Fatal("capability opened database")
	}
	if _, err := runSelfUpdateProbe(ctx, []string{"update-schema", "/tmp/evil"}, &out); err == nil {
		t.Fatal("probe accepted caller path")
	}
	if _, err := runSelfUpdateProbe(ctx, []string{"update-schema"}, &out); err == nil {
		t.Fatal("schema probe accepted absent database")
	}
	database, err := db.Open(ctx, filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	out.Reset()
	handled, err = runSelfUpdateProbe(ctx, []string{"update-schema"}, &out)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	if json.Unmarshal(out.Bytes(), &cap) != nil || cap.Schema != db.LatestVersion() {
		t.Fatal(out.String())
	}
}
