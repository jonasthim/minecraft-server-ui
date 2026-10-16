package safepath_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func TestOpenRootRejectsValidatedAncestorSubstitution(t *testing.T) {
	for _, area := range []string{"data", "instances", "instance", "server"} {
		t.Run(area, func(t *testing.T) {
			data := filepath.Join(t.TempDir(), "data")
			server := filepath.Join(data, "instances", "alpha", "server")
			if err := os.MkdirAll(server, 0700); err != nil {
				t.Fatal(err)
			}
			paths, err := instance.NewPaths(data)
			if err != nil {
				t.Fatal(err)
			}
			dirs, err := paths.For("alpha")
			if err != nil {
				t.Fatal(err)
			}
			selected := map[string]string{"data": data, "instances": filepath.Dir(dirs.Root), "instance": dirs.Root, "server": dirs.Server}[area]
			outside := t.TempDir()
			relative, err := filepath.Rel(selected, server)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(outside, relative)
			if err := os.MkdirAll(target, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target, "secret"), []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			// Prepare the symlink before moving the original so Windows privilege
			// failures skip without disrupting fixture cleanup.
			if err := os.Symlink(outside, selected+".link"); err != nil {
				t.Skip(err)
			}
			if err := os.Rename(selected, selected+".held"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				os.Remove(selected)
				if err := os.Rename(selected+".held", selected); err != nil {
					t.Error(err)
				}
			})
			if err := os.Rename(selected+".link", selected); err != nil {
				t.Fatal(err)
			}
			// This control establishes that the former bootstrap reaches outside.
			unchecked, err := os.OpenRoot(dirs.Server)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := unchecked.ReadFile("secret")
			unchecked.Close()
			if err != nil || string(raw) != "outside" {
				t.Fatal("fixture did not reproduce outside read", string(raw), err)
			}
			checked, err := safepath.OpenRoot(t.Context(), dirs.Server)
			if checked != nil {
				checked.Close()
				t.Fatal("unsafe root returned")
			}
			if err == nil {
				t.Fatal("validated ancestor substitution accepted")
			}
		})
	}
}

func TestOpenRootOrdinaryMissingCanceledAndCanonical(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "value"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := safepath.OpenRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := root.ReadFile("value")
	if err != nil || string(raw) != "inside" {
		t.Fatal(string(raw), err)
	}
	root.Close()
	// The returned handle continues to pin its directory after a rename.
	root, err = safepath.OpenRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	renamed := dir + ".held"
	if err = os.Rename(dir, renamed); err != nil {
		root.Close()
		t.Skip(err)
	}
	t.Cleanup(func() { os.Rename(renamed, dir) })
	raw, err = root.ReadFile("value")
	root.Close()
	if err != nil || string(raw) != "inside" {
		t.Fatal("returned root was not pinned", string(raw), err)
	}
	_, err = safepath.OpenRoot(t.Context(), filepath.Join(renamed, "missing", "child"))
	var pe *os.PathError
	if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pe) {
		t.Fatal("missing host cause lost", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = safepath.OpenRoot(ctx, renamed)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
	for _, bad := range []string{"relative", renamed + string(filepath.Separator) + ".", filepath.Join(renamed, "value")} {
		root, err = safepath.OpenRoot(t.Context(), bad)
		if root != nil {
			root.Close()
			t.Fatal("invalid path opened", bad)
		}
		if err == nil {
			t.Fatal("invalid path accepted", bad)
		}
	}
}

func TestOpenRootPreservesHostLegalDirectoryNames(t *testing.T) {
	// Colon and backslash are ordinary directory characters on Unix. The shared
	// bootstrap must not impose Windows filename rules on configured Unix roots.
	if filepath.Separator == '\\' {
		t.Skip("Unix filename behavior")
	}
	dir := filepath.Join(t.TempDir(), `configured:data\root`)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := safepath.OpenRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
}
