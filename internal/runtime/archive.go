package runtime

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxArchiveBytes   int64 = 768 << 20
	maxExtractedBytes int64 = 3 << 30
	maxEntryBytes     int64 = 1 << 30
	maxEntries              = 100000
)

// portablePath deliberately applies Windows naming rules on every host so an
// archive is never accepted on Linux and interpreted differently on Windows.
func portablePath(name string) (string, error) {
	if name == "" || !utf8.ValidString(name) || len(name) > 240 || strings.ContainsAny(name, "\\:\x00\r\n") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("unsafe archive path")
	}
	clean := strings.TrimSuffix(name, "/")
	if clean == "" || path.Clean(clean) != clean {
		return "", fmt.Errorf("non-canonical archive path")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == "." || part == ".." || len(part) > 120 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || strings.ContainsAny(part, `<>"|?*`) {
			return "", fmt.Errorf("unsafe archive component")
		}
		for _, c := range part {
			if c < 32 || c == 127 {
				return "", fmt.Errorf("control character in archive path")
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" {
			return "", fmt.Errorf("reserved Windows filename")
		}
		if len(base) >= 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && strings.Contains("123456789¹²³", base[3:]) {
			return "", fmt.Errorf("reserved Windows filename")
		}
	}
	return clean, nil
}

type archiveLink struct {
	name, target string
	hard         bool
}
type extractor struct {
	ctx     context.Context
	root    string
	total   int64
	count   int
	names   map[string]string
	entries map[string]bool
	links   []archiveLink
}

func (e *extractor) entry(name string, isDir bool) (string, error) {
	if err := e.ctx.Err(); err != nil {
		return "", err
	}
	e.count++
	if e.count > maxEntries {
		return "", fmt.Errorf("too many archive entries")
	}
	rel, err := portablePath(name)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(rel)
	if _, ok := e.entries[lower]; ok {
		return "", fmt.Errorf("duplicate archive entry")
	}
	e.entries[lower] = isDir
	parts := strings.Split(rel, "/")
	for i := range parts {
		prefix := strings.Join(parts[:i+1], "/")
		key := strings.ToLower(prefix)
		if old, ok := e.names[key]; ok && old != prefix {
			return "", fmt.Errorf("case-colliding archive paths")
		}
		e.names[key] = prefix
		if i < len(parts)-1 {
			if dir, ok := e.entries[key]; ok && !dir {
				return "", fmt.Errorf("archive file used as directory")
			}
		}
	}
	dest := filepath.Join(e.root, filepath.FromSlash(rel))
	if len(dest) > 240 && hostOS() == "windows" {
		return "", fmt.Errorf("archive path exceeds Windows path limit")
	}
	return dest, nil
}
func (e *extractor) file(dest string, size int64, mode os.FileMode, r io.Reader) error {
	if size < 0 || size > maxEntryBytes || e.total > maxExtractedBytes-size {
		return fmt.Errorf("archive expansion limit exceeded")
	}
	e.total += size
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	perm := os.FileMode(0600)
	if mode&0111 != 0 {
		perm = 0700
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	n, copyErr := io.Copy(f, &contextReader{ctx: e.ctx, r: io.LimitReader(r, size+1)})
	closeErr := f.Close()
	if copyErr != nil {
		return fmt.Errorf("extract archive entry: %w", copyErr)
	}
	if n != size {
		return fmt.Errorf("archive entry size mismatch")
	}
	if closeErr != nil {
		return fmt.Errorf("close archive entry: %w", closeErr)
	}
	return nil
}
func extractArchive(ctx context.Context, archive, format, dest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e := &extractor{ctx: ctx, root: dest, names: make(map[string]string), entries: make(map[string]bool)}
	switch format {
	case "tar.gz":
		f, err := os.Open(archive)
		if err != nil {
			return fmt.Errorf("open archive: %w", err)
		}
		defer f.Close()
		g, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("open gzip: %w", err)
		}
		defer g.Close()
		// Bound every decompressed byte, including PAX headers and padding that
		// archive/tar may consume internally before returning an entry.
		decompressed := &io.LimitedReader{R: &contextReader{ctx: ctx, r: g}, N: maxExtractedBytes + maxEntries*2048 + 1}
		tr := tar.NewReader(decompressed)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return fmt.Errorf("read tar: %w", err)
			}
			if h.Size < 0 || h.Size > maxEntryBytes || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Size != 0) {
				return fmt.Errorf("invalid tar entry size")
			}
			dir := h.Typeflag == tar.TypeDir
			target, err := e.entry(h.Name, dir)
			if err != nil {
				return err
			}
			switch h.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(target, 0700); err != nil {
					return fmt.Errorf("extract directory: %w", err)
				}
			case tar.TypeReg, tar.TypeRegA:
				if err := e.file(target, h.Size, os.FileMode(h.Mode), tr); err != nil {
					return err
				}
			case tar.TypeSymlink, tar.TypeLink:
				e.links = append(e.links, archiveLink{name: h.Name, target: h.Linkname, hard: h.Typeflag == tar.TypeLink})
			default:
				return fmt.Errorf("unsupported tar entry type")
			}
		}
		// Drain gzip to enforce its CRC and reject truncated trailers as well as
		// trailing decompression bombs that tar's end marker would otherwise hide.
		n, err := io.Copy(io.Discard, &contextReader{ctx: ctx, r: io.LimitReader(decompressed, 1<<20+1)})
		if err != nil {
			return fmt.Errorf("verify gzip trailer: %w", err)
		}
		if n > 1<<20 {
			return fmt.Errorf("excess data after tar archive")
		}
	case "zip":
		z, err := zip.OpenReader(archive)
		if err != nil {
			return fmt.Errorf("open zip: %w", err)
		}
		defer z.Close()
		for _, f := range z.File {
			if f.Mode()&os.ModeSymlink != 0 || !f.Mode().IsRegular() && !f.Mode().IsDir() {
				return fmt.Errorf("zip links and special files are unsupported")
			}
			target, err := e.entry(f.Name, f.FileInfo().IsDir())
			if err != nil {
				return err
			}
			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(target, 0700); err != nil {
					return fmt.Errorf("extract directory: %w", err)
				}
				continue
			}
			if f.UncompressedSize64 > uint64(maxEntryBytes) {
				return fmt.Errorf("archive entry too large")
			}
			r, err := f.Open()
			if err != nil {
				return fmt.Errorf("open zip entry: %w", err)
			}
			copyErr := e.file(target, int64(f.UncompressedSize64), f.Mode(), r)
			closeErr := r.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return fmt.Errorf("close zip entry: %w", closeErr)
			}
		}
	default:
		return fmt.Errorf("unsupported runtime archive format")
	}
	// Official Linux JDKs contain relative license links. Copy their validated
	// regular-file targets rather than creating any filesystem links.
	for len(e.links) > 0 {
		pending := e.links
		e.links = nil
		advanced := false
		for _, l := range pending {
			if err := ctx.Err(); err != nil {
				return err
			}
			if l.target == "" || strings.ContainsAny(l.target, "\\:\x00") || path.IsAbs(l.target) {
				return fmt.Errorf("unsafe archive link")
			}
			rel := l.target
			if !l.hard {
				rel = path.Join(path.Dir(l.name), l.target)
			}
			rel, err := portablePath(rel)
			if err != nil {
				return fmt.Errorf("unsafe archive link: %w", err)
			}
			source := filepath.Join(dest, filepath.FromSlash(rel))
			info, err := os.Lstat(source)
			if os.IsNotExist(err) {
				e.links = append(e.links, l)
				continue
			}
			if err != nil {
				return fmt.Errorf("read archive link target: %w", err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("archive link target is not a regular file")
			}
			in, err := os.Open(source)
			if err != nil {
				return fmt.Errorf("open archive link target: %w", err)
			}
			copyErr := e.file(filepath.Join(dest, filepath.FromSlash(l.name)), info.Size(), info.Mode(), in)
			_ = in.Close()
			if copyErr != nil {
				return copyErr
			}
			advanced = true
		}
		if !advanced {
			return fmt.Errorf("archive contains cyclic or missing link targets")
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
