package world

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func missingPersistedRootFixture(t *testing.T) *Service {
	t.Helper()
	s, base := exportFixture(t)
	if _, err := s.db.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port,config_json) VALUES('alpha','Alpha','paper','1.21.9',21,1024,25565,'{"level_name":"My world"}')`); err != nil {
		t.Fatal(err)
	}
	s.instances = instance.New(s.db, s.paths, nil)
	if err := os.RemoveAll(filepath.Dir(base)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiscoverMissingPersistedInstanceRootIsAbsent(t *testing.T) {
	s := missingPersistedRootFixture(t)
	d, err := s.Discover(context.Background(), "alpha")
	if err != nil || d.State != "absent" {
		t.Fatalf("missing persisted root: state=%q err=%v", d.State, err)
	}
}

func TestCaptureMissingPersistedInstanceRootIsNotFound(t *testing.T) {
	s := missingPersistedRootFixture(t)
	a, err := s.Capture(context.Background(), operator, "alpha")
	if a != nil {
		a.Close()
		t.Fatal("missing world returned archive")
	}
	if err == nil || domain.AsError(err).Code != domain.CodeNotFound {
		t.Fatal("missing saved world is not not_found", err)
	}
}

// These substitutions occur after Paths.For's string validation and before the
// first filesystem open, reproducing the unsafe bootstrap window deterministically.
func TestWorldRootRejectsAncestorSwapAfterPathValidation(t *testing.T) {
	for _, ancestor := range []string{"data", "instances"} {
		t.Run(ancestor, func(t *testing.T) {
			s, base := exportFixture(t)
			dirs, err := s.paths.For("alpha")
			if err != nil {
				t.Fatal(err)
			}
			original := filepath.Dir(dirs.Root)
			if ancestor == "data" {
				original = filepath.Dir(original)
			}
			outside := t.TempDir()
			relative, err := filepath.Rel(original, dirs.Server)
			if err != nil {
				t.Fatal(err)
			}
			put(t, outside, filepath.ToSlash(filepath.Join(relative, "My world", "level.dat")), "outside secret world")
			if err := os.Rename(original, original+".held"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.Remove(original); err != nil && !os.IsNotExist(err) {
					t.Error(err)
				}
				if err := os.Rename(original+".held", original); err != nil {
					t.Error(err)
				}
			})
			if err := os.Symlink(outside, original); err != nil {
				t.Skip(err)
			}
			root, err := openWorldRoot(dirs.Root)
			if err == nil {
				defer root.Close()
				data, readErr := root.ReadFile("server/My world/level.dat")
				t.Fatalf("ancestor substitution admitted outside world: %q %v (original %s)", data, readErr, base)
			}
			if root != nil {
				root.Close()
				t.Fatal("failed bootstrap returned usable root")
			}
			if _, err := s.Discover(context.Background(), "alpha"); err == nil {
				t.Fatal("outside world discovered")
			}
			archive, err := s.Capture(context.Background(), operator, "alpha")
			if archive != nil {
				archive.Close()
				t.Fatal("outside world exported")
			}
			if err == nil {
				t.Fatal("substituted-root capture succeeded")
			}
		})
	}
}

func TestWorldRootNormalAndHostErrors(t *testing.T) {
	s, _ := exportFixture(t)
	dirs, err := s.paths.For("alpha")
	if err != nil {
		t.Fatal(err)
	}
	r, err := openWorldRoot(dirs.Root)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := r.ReadFile("server/My world/level.dat"); err != nil || string(raw) != "level" {
		t.Fatal(string(raw), err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{filepath.Join(dirs.Root, "missing", "child"), filepath.Join(dirs.Root, "missing")} {
		opened, err := openWorldRoot(target)
		if opened != nil {
			opened.Close()
			t.Fatal("invalid directory opened", target)
		}
		if err == nil {
			t.Fatal("host error swallowed", target)
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Fatal("host error detail lost", err)
		}
	}
	opened, err := openChild(r, "server")
	if opened != nil {
		opened.Close()
		t.Fatal("closed parent returned child")
	}
	if !errors.Is(err, os.ErrClosed) {
		t.Fatal("closed root error lost", err)
	}
}

func TestWorldRootRejectsNoncanonicalAndNondirectoryPaths(t *testing.T) {
	s, _ := exportFixture(t)
	dirs, err := s.paths.For("alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"", ".", "relative/world", dirs.Root + string(filepath.Separator) + "..", filepath.Join(dirs.Server, "server.properties")} {
		r, err := openWorldRoot(target)
		if r != nil {
			r.Close()
			t.Fatal("invalid bootstrap returned root", target)
		}
		if err == nil {
			t.Fatal("unsafe root accepted", target)
		}
	}
}
