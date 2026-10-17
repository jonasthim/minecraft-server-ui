package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
)

func TestManagerStateGrammarRejectsExtraOrMalformedArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"snapshot", "extra"}, {"verify"}, {"verify", "--snapshot"},
		{"verify", "--snapshot", "../outside"}, {"verify", "--snapshot", strings.Repeat("A", 64)},
		{"migrate"}, {"snapshot", "--output", "/tmp/out"},
	} {
		var output bytes.Buffer
		if err := runManagerState(context.Background(), args, &output); err == nil || output.Len() != 0 {
			t.Fatalf("accepted arguments %q or wrote output: %s", args, output.String())
		}
	}
}

func TestManagerStateCommandEmitsOnlyVerifiedSummary(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateCommandTestDirectory(t, dir)
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var output bytes.Buffer
	if err := run(t.Context(), []string{"manager-state", "snapshot"}, &output); err != nil {
		t.Fatal(err)
	}
	var summary managerstate.Summary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal("command did not emit a JSON summary")
	}
	if summary.ID == "" || summary.Schema != db.LatestVersion() || strings.Contains(output.String(), dir) || strings.Contains(output.String(), "settings") {
		t.Fatalf("unsafe or incomplete summary: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "maintenance", summary.ID, "snapshot.json")); err != nil {
		t.Fatal("verified summary lacks its published manifest")
	}
	output.Reset()
	if err := run(t.Context(), []string{"manager-state", "verify", "--snapshot", summary.ID}, &output); err != nil {
		t.Fatal(err)
	}
	var verified managerstate.Summary
	if err := json.Unmarshal(output.Bytes(), &verified); err != nil || verified != summary {
		t.Fatal("dispatched verification did not return the original summary")
	}
}
