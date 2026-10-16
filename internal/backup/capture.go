package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type limits struct {
	entries, depth                            int
	bytes, fileBytes, archiveBytes, pathBytes int64
}

var defaultLimits = limits{100000, 32, 16 << 30, 8 << 30, (16 << 30) + (32 << 20), 8 << 20}
var errCorrupt = domain.E(domain.CodeValidationFailed, "backup contains unsafe, inconsistent, or corrupt data")
var errStale = domain.E(domain.CodeConflict, "backup source changed during capture")
var errUnsupported = domain.E(domain.CodeUnsupported, "backup filesystem is not qualified")
var reserved = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

func safePath(p string) error {
	if p == "" || len(p) > 4096 || !utf8.ValidString(p) {
		return errCorrupt
	}
	for _, v := range strings.Split(p, "/") {
		if v == "" || v == "." || v == ".." || len(v) > 255 || strings.TrimRight(v, " .") != v || strings.ContainsAny(v, "\\:<>\"|?*\x00") || reserved.MatchString(v) {
			return errCorrupt
		}
		for _, r := range v {
			if r < 32 || r == 127 {
				return errCorrupt
			}
		}
	}
	return nil
}
func excluded(p string) bool {
	parts := strings.Split(strings.ToLower(p), "/")
	for _, v := range parts {
		if v == "manager.db" || strings.HasPrefix(v, "manager.db-") || v == "session.lock" || v == ".minecraft-ui" || v == ".manager" {
			return true
		}
	}
	// Launcher/control state and the manager backup tree are reserved at the
	// server root. Ordinary addon files with these basenames stay local data.
	if len(parts) > 0 {
		switch parts[0] {
		case "backups", "launcher.json", "control.json", "identity.json", "launch.json":
			return true
		}
	}
	return false
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	if len(p) > 32768 {
		p = p[:32768]
	}
	return r.r.Read(p)
}

// captureTree uses only pinned directory capabilities. No caller-selected host
// path or link target enters staging. Files are hashed and reread after copying;
// file replacement, growth and same-size in-place edits invalidate the capture.
func captureTree(ctx context.Context, src, dst *source, roots []string, full bool, lim limits) ([]File, error) {
	files := []File{}
	seen := map[string]bool{}
	count := 0
	var total, pathBytes int64
	var walk func(*os.Root, string, int) error
	walk = func(parent *os.Root, name string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > lim.depth {
			return errCorrupt
		}
		if err := safePath(name); err != nil {
			return err
		}
		if excluded(name) {
			return nil
		}
		fold := strings.ToLower(name)
		if seen[fold] {
			return errCorrupt
		}
		seen[fold] = true
		count++
		pathBytes += int64(len(name))
		if count > lim.entries || pathBytes > lim.pathBytes {
			return errCorrupt
		}
		// Descend from the source root for each entry; every ancestor stays pinned
		// until this file has been checked and copied.
		child := &source{root: src.root}
		defer child.Close()
		base := path.Base(name)
		if dir := path.Dir(name); dir != "." {
			if err := child.descend(dir); err != nil {
				return err
			}
		}
		before, err := child.root.Lstat(base)
		if err != nil {
			return err
		}
		if !safeInfo(before) {
			return errCorrupt
		}
		if before.IsDir() {
			next, err := child.root.OpenRoot(base)
			if err != nil {
				return err
			}
			defer next.Close()
			after, err := next.Stat(".")
			if err != nil {
				return err
			}
			current, err := child.root.Lstat(base)
			if err != nil {
				return err
			}
			if !os.SameFile(before, after) || !os.SameFile(before, current) || !safeInfo(current) {
				return errStale
			}
			if err := dst.root.MkdirAll(name, 0700); err != nil {
				return err
			}
			entries, err := boundedReadDir(next, lim.entries-count)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				if err := walk(next, path.Join(name, entry.Name()), depth+1); err != nil {
					return err
				}
			}
			current, err = child.root.Lstat(base)
			if err != nil {
				return err
			}
			if !os.SameFile(before, current) || !current.ModTime().Equal(before.ModTime()) {
				return errStale
			}
			return child.verifyLinks()
		}
		if !before.Mode().IsRegular() || before.Size() > lim.fileBytes || before.Size() < 0 || before.Size() > lim.bytes-total {
			return errCorrupt
		}
		total += before.Size()
		saved, err := child.open(base, lim.fileBytes)
		if err != nil {
			return err
		}
		defer saved.f.Close()
		if dir := path.Dir(name); dir != "." {
			if err := dst.root.MkdirAll(dir, 0700); err != nil {
				return err
			}
		}
		out, err := dst.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, hash), contextReader{ctx, io.LimitReader(saved.f, before.Size()+1)})
		syncErr := out.Sync()
		closeErr := out.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
		if n != before.Size() {
			return errStale
		}
		if _, err := saved.f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		again := sha256.New()
		n, err = io.Copy(again, contextReader{ctx, io.LimitReader(saved.f, before.Size()+1)})
		if err != nil {
			return err
		}
		if n != before.Size() || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(again.Sum(nil))) {
			return errStale
		}
		opened, err := saved.f.Stat()
		if err != nil {
			return err
		}
		current, err := child.root.Lstat(base)
		if err != nil {
			return err
		}
		identity, err := fileIdentity(saved.f)
		if err != nil {
			return err
		}
		if identity != saved.identity || !safeInfo(current) || !os.SameFile(before, opened) || !os.SameFile(before, current) || opened.Size() != before.Size() || current.Size() != before.Size() || !opened.ModTime().Equal(before.ModTime()) || !current.ModTime().Equal(before.ModTime()) {
			return errStale
		}
		if err := child.verifyLinks(); err != nil {
			return err
		}
		files = append(files, File{name, n, hex.EncodeToString(hash.Sum(nil))})
		return nil
	}
	if full {
		entries, err := boundedReadDir(src.root, lim.entries)
		if err != nil {
			return nil, err
		}
		roots = nil
		for _, v := range entries {
			roots = append(roots, v.Name())
		}
	}
	for _, name := range roots {
		if strings.Contains(name, "/") {
			return nil, errCorrupt
		}
		if err := walk(src.root, name, 0); err != nil {
			return nil, err
		}
	}
	if err := src.verify(); err != nil {
		return nil, err
	}
	if err := dst.verify(); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}
func (s *source) verifyLinks() error {
	for _, link := range s.links {
		current, e := link.parent.Lstat(link.name)
		if e != nil {
			return e
		}
		if !safeInfo(current) || !os.SameFile(link.info, current) {
			return errStale
		}
	}
	return nil
}
