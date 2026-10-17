package mapdata

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func TestPinnedSourceDetectsSameTimestampMutation(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "data")
	if e := os.WriteFile(name, []byte("before"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f, e := s.open("data", 100)
	if e != nil {
		t.Fatal(e)
	}
	defer f.f.Close()
	b, e := f.readAt(context.Background(), 0, 6)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(name, []byte("after!"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(name, time.Now(), f.info.ModTime()); e != nil {
		t.Fatal(e)
	}
	if e = f.verify(context.Background(), []span{{offset: 0, data: b}}); e == nil {
		t.Fatal("same size/timestamp changed bytes accepted")
	}
}
func TestPinnedSourceRejectsLinksAndDirectoryReplacement(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "data"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(outside, filepath.Join(dir, "link")); e != nil {
		t.Skip(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.descend("link"); e == nil {
		t.Fatal("directory symlink accepted")
	}
	if e = os.Mkdir(filepath.Join(dir, "region"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = s.descend("region"); e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(filepath.Join(dir, "region"), filepath.Join(dir, "old")); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(filepath.Join(dir, "region"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = s.verify(); e == nil {
		t.Fatal("replaced directory accepted")
	}
}

// Paths.For validates strings, but an ancestor can change before the decoder's
// first open. Matching final identities alone must never admit the outside tree.
func TestSourceRejectsAncestorSwapAfterInstancePathValidation(t *testing.T) {
	data := t.TempDir()
	paths, e := instance.NewPaths(data)
	if e != nil {
		t.Fatal(e)
	}
	dirs, e := paths.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(dirs.Server, 0700); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e = os.Mkdir(filepath.Join(outside, "server"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(outside, "server", "secret"), []byte("outside bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	// Complete the same path validation used immediately before openSource.
	dirs, e = paths.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(dirs.Root, dirs.Root+".held"); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, dirs.Root); e != nil {
		t.Skip(e)
	}
	s, e := openSource(dirs.Server)
	if e == nil {
		s.Close()
		t.Fatal("ancestor substitution admitted an outside server directory")
	}
}

func TestSavedReadPreservesHostErrorsAndClassifiesTruncation(t *testing.T) {
	for _, closed := range []bool{true, false} {
		t.Run(fmt.Sprint("closed=", closed), func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "data")
			if e := os.WriteFile(name, []byte("before"), 0600); e != nil {
				t.Fatal(e)
			}
			s, e := openSource(dir)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			f, e := s.open("data", 100)
			if e != nil {
				t.Fatal(e)
			}
			defer f.f.Close()
			if closed {
				if e = f.f.Close(); e != nil {
					t.Fatal(e)
				}
			} else {
				if e = os.Truncate(name, 3); e != nil {
					t.Fatal(e)
				}
			}
			_, e = f.readAt(context.Background(), 0, 6)
			if closed {
				if !errors.Is(e, os.ErrClosed) || errors.Is(e, errStale) {
					t.Fatalf("host read error lost: %v", e)
				}
				_, _, visible := status(e)
				if !errors.Is(visible, os.ErrClosed) {
					t.Fatalf("host error swallowed by status: %v", visible)
				}
			} else if !errors.Is(e, errStale) {
				t.Fatalf("observed truncation was not stale: %v", e)
			}
		})
	}
}

func TestSourceVerificationPreservesClosedHandleErrors(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "data"), []byte("safe"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openSource(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	f, e := s.open("data", 100)
	if e != nil {
		t.Fatal(e)
	}
	defer f.f.Close()
	if e = s.root.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.verify(context.Background(), nil); !errors.Is(e, os.ErrClosed) || errors.Is(e, errStale) {
		t.Fatalf("file verification host error lost: %v", e)
	}
	if e = s.roots[0].Close(); e != nil {
		t.Fatal(e)
	}
	if e = s.verify(); !errors.Is(e, os.ErrClosed) || errors.Is(e, errStale) {
		t.Fatalf("directory verification host error lost: %v", e)
	}
}

func TestSourceRetainsAndRechecksAncestorAfterOpen(t *testing.T) {
	data := t.TempDir()
	server := filepath.Join(data, "instance", "server")
	if e := os.MkdirAll(server, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(server, "data"), []byte("inside"), 0600); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e := os.Mkdir(filepath.Join(outside, "server"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(outside, "server", "data"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	s, e := openSource(server)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ancestor := filepath.Dir(server)
	renameErr := os.Rename(ancestor, ancestor+".held")
	if renameErr != nil {
		if runtime.GOOS != "windows" || !os.IsPermission(renameErr) {
			t.Fatal(renameErr)
		}
		// Windows can deny moving a directory with retained open descendants.
		// Prove that the attack was prevented rather than skipping the check.
		if _, e = os.Lstat(ancestor + ".held"); !os.IsNotExist(e) {
			t.Fatalf("denied rename created a destination: %v", e)
		}
		t.Logf("native filesystem prevented pinned ancestor rename: %v", renameErr)
	} else if e = os.Symlink(outside, ancestor); e != nil {
		t.Skip(e)
	}
	f, e := s.open("data", 100)
	if e != nil {
		t.Fatal(e)
	}
	defer f.f.Close()
	b, e := f.readAt(context.Background(), 0, 6)
	if e != nil || string(b) != "inside" {
		t.Fatalf("source escaped pinned ancestor: %q %v", b, e)
	}
	if e = s.verify(); renameErr != nil {
		if e != nil {
			t.Fatalf("denied rename changed pinned tree: %v", e)
		}
	} else if !errors.Is(e, errStale) {
		t.Fatalf("ancestor change was not stale: %v", e)
	}
}
