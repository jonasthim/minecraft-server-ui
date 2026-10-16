package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type bootstrapContext struct {
	context.Context
	calls  int
	at     int
	change func()
}

func (c *bootstrapContext) Err() error {
	c.calls++
	if c.calls == c.at {
		c.change()
	}
	return nil
}

func bootstrapSubstitute(t *testing.T, original, outside string) func() {
	t.Helper()
	link := original + ".link"
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	return func() {
		if err := os.Rename(original, original+".held"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			os.Remove(original)
			if err := os.Rename(original+".held", original); err != nil {
				t.Error(err)
			}
		})
		if err := os.Rename(link, original); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCreateRejectsAncestorSubstitutionBeforeParentCreation(t *testing.T) {
	s, paths, _ := setup(t)
	outside := t.TempDir()
	ctx := &bootstrapContext{Context: t.Context(), at: 2, change: bootstrapSubstitute(t, paths.dataDir, outside)}
	if _, err := s.Create(ctx, admin, draft("alpha", 25565)); err == nil {
		t.Fatal("substituted data directory admitted")
	}
	if ctx.calls < 2 {
		t.Fatal("substitution window not reached")
	}
	if _, err := os.Stat(filepath.Join(outside, "instances")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created parent outside data", err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM instances`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed create persisted instance", count, err)
	}
}
func TestMaterializeMissingRootPreservesMissingCause(t *testing.T) {
	s, paths, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	dirs, err := paths.For("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(dirs.Root); err != nil {
		t.Fatal(err)
	}
	if err = s.materialize(t.Context(), "alpha"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing materialization root lost cause", err)
	}
}

func TestCreatePreservesMissingConfiguredDataDirectory(t *testing.T) {
	s, paths, _ := setup(t)
	missing := filepath.Join(paths.dataDir, "not-yet-created", "data")
	var err error
	s.paths, err = NewPaths(missing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal("first-use directory creation regressed", err)
	}
	if _, err = os.Stat(filepath.Join(missing, "instances", "alpha", "state", "instance-owner")); err != nil {
		t.Fatal(err)
	}
}
