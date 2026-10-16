package mapdata

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

type directoryLink struct {
	parent, child *os.Root
	name          string
	info          fs.FileInfo
}
type source struct {
	absolute string
	initial  fs.FileInfo
	roots    []*os.Root
	links    []directoryLink
	root     *os.Root
}

func openSource(absolute string) (*source, error) {
	before, e := os.Lstat(absolute)
	if e != nil {
		return nil, e
	}
	if !before.IsDir() || !safeInfo(before) {
		return nil, errCorrupt
	}
	r, e := os.OpenRoot(absolute)
	if e != nil {
		return nil, e
	}
	s := &source{absolute: absolute, initial: before, roots: []*os.Root{r}, root: r}
	after, e := r.Stat(".")
	current, ce := os.Lstat(absolute)
	if e != nil || ce != nil || !os.SameFile(before, after) || !os.SameFile(before, current) || !safeInfo(current) {
		s.Close()
		return nil, errStale
	}
	return s, nil
}
func (s *source) Close() {
	for i := len(s.roots) - 1; i >= 0; i-- {
		s.roots[i].Close()
	}
}
func (s *source) descend(relative string) error {
	for _, name := range strings.Split(relative, "/") {
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "\\:\x00") {
			return errCorrupt
		}
		before, e := s.root.Lstat(name)
		if e != nil {
			return e
		}
		if !before.IsDir() || !safeInfo(before) {
			return errCorrupt
		}
		r, e := s.root.OpenRoot(name)
		if e != nil {
			return e
		}
		after, e := r.Stat(".")
		current, ce := s.root.Lstat(name)
		if e != nil || ce != nil || !os.SameFile(before, after) || !os.SameFile(before, current) || !safeInfo(current) {
			r.Close()
			return errStale
		}
		s.links = append(s.links, directoryLink{s.root, r, name, before})
		s.roots = append(s.roots, r)
		s.root = r
	}
	return nil
}
func (s *source) verify() error {
	current, e := os.Lstat(s.absolute)
	if e != nil || !safeInfo(current) || !os.SameFile(s.initial, current) {
		return errStale
	}
	for _, link := range s.links {
		current, e := link.parent.Lstat(link.name)
		if e != nil || !safeInfo(current) || !os.SameFile(link.info, current) {
			return errStale
		}
	}
	return nil
}

type savedFile struct {
	root     *os.Root
	name     string
	f        *os.File
	info     fs.FileInfo
	identity string
}

func (s *source) open(name string, maxSize int64) (*savedFile, error) {
	if strings.ContainsAny(name, "/\\:\x00") || name == "." || name == ".." {
		return nil, errCorrupt
	}
	before, e := s.root.Lstat(name)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() || !safeInfo(before) || before.Size() > maxSize {
		return nil, errCorrupt
	}
	f, e := openRead(s.root, name)
	if e != nil {
		return nil, e
	}
	after, e := f.Stat()
	current, ce := s.root.Lstat(name)
	if e != nil || ce != nil || !after.Mode().IsRegular() || !safeInfo(current) || !os.SameFile(before, after) || !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		f.Close()
		return nil, errStale
	}
	identity, e := fileIdentity(f)
	if e != nil {
		f.Close()
		return nil, e
	}
	return &savedFile{s.root, name, f, after, identity}, nil
}

type span struct {
	offset int64
	data   []byte
}

func (f *savedFile) readAt(ctx context.Context, offset int64, n int) ([]byte, error) {
	if n < 0 || n > 16<<20 || offset < 0 || offset > f.info.Size() || int64(n) > f.info.Size()-offset {
		return nil, errCorrupt
	}
	b := make([]byte, n)
	for pos := 0; pos < n; {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		end := min(pos+32768, n)
		_, e := f.f.ReadAt(b[pos:end], offset+int64(pos))
		if e != nil {
			return nil, fmt.Errorf("saved data read: %w", errStale)
		}
		pos = end
	}
	return b, nil
}
func (f *savedFile) verify(ctx context.Context, spans []span) error {
	for _, s := range spans {
		b, e := f.readAt(ctx, s.offset, len(s.data))
		if e != nil {
			return e
		}
		if !bytes.Equal(b, s.data) {
			return errStale
		}
	}
	identity, e := fileIdentity(f.f)
	if e != nil {
		return e
	}
	if identity != f.identity {
		return errStale
	}
	opened, e := f.f.Stat()
	current, ce := f.root.Lstat(f.name)
	if e != nil || ce != nil || !safeInfo(current) || !os.SameFile(f.info, opened) || !os.SameFile(f.info, current) || opened.Size() != f.info.Size() || current.Size() != f.info.Size() || !opened.ModTime().Equal(f.info.ModTime()) || !current.ModTime().Equal(f.info.ModTime()) {
		return errStale
	}
	return nil
}
