package mapdata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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

// openSource anchors at the filesystem root (or local Windows drive root),
// then pins every path component. A final-path Lstat/OpenRoot pair alone would
// accept an ancestor replaced with a symlink before that first Lstat.
func openSource(absolute string) (*source, error) {
	if !filepath.IsAbs(absolute) {
		return nil, errCorrupt
	}
	volume := filepath.VolumeName(absolute)
	// UNC, device and extended-volume paths need their own trusted-root policy;
	// reject them rather than treating an arbitrary share as a drive root.
	if volume != "" && (len(volume) != 2 || volume[1] != ':' || !(volume[0] >= 'a' && volume[0] <= 'z' || volume[0] >= 'A' && volume[0] <= 'Z')) {
		return nil, errUnsupported
	}
	anchor := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, anchor)
	if relative == absolute {
		return nil, errCorrupt
	}
	before, e := os.Lstat(anchor)
	if e != nil {
		return nil, e
	}
	if !before.IsDir() || !safeInfo(before) {
		return nil, errCorrupt
	}
	r, e := os.OpenRoot(anchor)
	if e != nil {
		return nil, e
	}
	s := &source{absolute: anchor, initial: before, roots: []*os.Root{r}, root: r}
	after, e := r.Stat(".")
	current, ce := os.Lstat(anchor)
	if e != nil {
		s.Close()
		return nil, fmt.Errorf("stat source root: %w", e)
	}
	if ce != nil {
		s.Close()
		return nil, observedLookupError("recheck source root", ce)
	}
	if !os.SameFile(before, after) || !os.SameFile(before, current) || !safeInfo(current) {
		s.Close()
		return nil, errStale
	}
	if relative != "" {
		if e := s.descend(filepath.ToSlash(relative)); e != nil {
			s.Close()
			return nil, e
		}
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
		if e != nil {
			r.Close()
			return fmt.Errorf("stat source directory: %w", e)
		}
		if ce != nil {
			r.Close()
			return observedLookupError("recheck source directory", ce)
		}
		if !os.SameFile(before, after) || !os.SameFile(before, current) || !safeInfo(current) {
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
	if e != nil {
		return observedLookupError("verify source root", e)
	}
	if !safeInfo(current) || !os.SameFile(s.initial, current) {
		return errStale
	}
	for _, link := range s.links {
		current, e := link.parent.Lstat(link.name)
		if e != nil {
			return observedLookupError("verify source directory", e)
		}
		if !safeInfo(current) || !os.SameFile(link.info, current) {
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
	if e != nil {
		f.Close()
		return nil, fmt.Errorf("stat saved file: %w", e)
	}
	if ce != nil {
		f.Close()
		return nil, observedLookupError("recheck saved file", ce)
	}
	if !after.Mode().IsRegular() || !safeInfo(current) || !os.SameFile(before, after) || !os.SameFile(before, current) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
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
			if errors.Is(e, io.EOF) || errors.Is(e, io.ErrUnexpectedEOF) {
				return nil, fmt.Errorf("saved data truncated during read: %w", errors.Join(errStale, e))
			}
			return nil, fmt.Errorf("saved data read: %w", e)
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
	if e != nil {
		return fmt.Errorf("verify opened saved file: %w", e)
	}
	if ce != nil {
		return observedLookupError("verify saved file path", ce)
	}
	if !safeInfo(current) || !os.SameFile(f.info, opened) || !os.SameFile(f.info, current) || opened.Size() != f.info.Size() || current.Size() != f.info.Size() || !opened.ModTime().Equal(f.info.ModTime()) || !current.ModTime().Equal(f.info.ModTime()) {
		return errStale
	}
	return nil
}

// Disappearance of a previously pinned object is an observed change. Other
// lookup failures (permissions, closed handles, device errors) remain host errors.
func observedLookupError(operation string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: %w", operation, errors.Join(errStale, err))
	}
	return fmt.Errorf("%s: %w", operation, err)
}
