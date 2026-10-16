package lifecycle

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

func TestRemoveActivationRejectsAncestorSubstitution(t *testing.T) {
	s, _, _ := fixture(t)
	outside := t.TempDir()
	state := filepath.Join(outside, "instances", "test", "state")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(state, "launch.json")
	if err := os.WriteFile(marker, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &bootstrapContext{Context: t.Context(), at: 1, change: bootstrapSubstitute(t, s.dataDir, outside)}
	if err := s.removeActivation(ctx, "test"); err == nil {
		t.Fatal("outside activation removal admitted")
	}
	if ctx.calls == 0 {
		t.Fatal("substitution window not reached")
	}
	if raw, err := os.ReadFile(marker); err != nil || string(raw) != "outside" {
		t.Fatal("outside activation changed", string(raw), err)
	}
}
func TestRemoveActivationMissingRootPreservesCause(t *testing.T) {
	s, _, _ := fixture(t)
	if err := os.RemoveAll(filepath.Join(s.dataDir, "instances", "test", "state")); err != nil {
		t.Fatal(err)
	}
	if err := s.removeActivation(t.Context(), "test"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing state cause lost", err)
	}
}
