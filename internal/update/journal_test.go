package update

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func TestJournalPublicationAndRecoveryCASNeverTouchesWorlds(t *testing.T) {
	root := t.TempDir()
	paths, _ := instance.NewPaths(root)
	d, _ := paths.For("alpha")
	if err := os.MkdirAll(filepath.Join(d.Server, "mods"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(d.Server, "world"), 0700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d.Server, "world", "sentinel"), []byte("world"), 0600)
	os.WriteFile(filepath.Join(d.Server, "mods", "a.jar"), []byte("before"), 0600)
	os.MkdirAll(d.State, 0700)
	s := &Service{config: Config{Paths: paths}}
	j := Journal{Version: 1, Operations: []JournalOperation{{Path: "mods/a.jar", Before: hashBytes([]byte("before")), After: hashBytes([]byte("after")), BeforeBlob: "before-0", AfterBlob: "after-0"}}}
	tx := strings.Repeat("a", 32)
	if err := s.stageJournalBlob(t.Context(), "alpha", tx, "before-0", j.Operations[0].Before, strings.NewReader("before")); err != nil {
		t.Fatal(err)
	}
	if err := s.stageJournalBlob(t.Context(), "alpha", tx, "after-0", j.Operations[0].After, strings.NewReader("after")); err != nil {
		t.Fatal(err)
	}
	if err := s.applyJournalFiles(t.Context(), "alpha", tx, j, true); err != nil {
		t.Fatal(err)
	}
	if err := s.applyJournalFiles(t.Context(), "alpha", tx, j, true); err != nil {
		t.Fatal("replay", err)
	}
	if err := s.applyJournalFiles(t.Context(), "alpha", tx, j, false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(d.Server, "mods", "a.jar"), []byte("foreign"), 0600)
	if err := s.applyJournalFiles(t.Context(), "alpha", tx, j, true); err == nil {
		t.Fatal("foreign bytes overwritten")
	}
	j.Operations[0].Path = "world/sentinel"
	if err := s.applyJournalFiles(context.Background(), "alpha", tx, j, true); err == nil {
		t.Fatal("world path admitted")
	}
	b, _ := os.ReadFile(filepath.Join(d.Server, "world", "sentinel"))
	if string(b) != "world" {
		t.Fatal("world changed")
	}
}
