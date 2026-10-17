package bootstrap

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

func extract(ctx context.Context, d *destination, a candidate.Bundle, f *os.File) error {
	consume := func(index int, name string, size int64, mode os.FileMode, r io.Reader) error {
		if index >= len(a.Members) {
			return errors.New("extra member")
		}
		want := a.Members[index]
		if name != want.Name || size != want.Size || !mode.IsRegular() || mode.Perm() != os.FileMode(want.Mode) || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("member identity changed")
		}
		// Only the already authenticated, fixed flat name reaches OS operations.
		out, e := d.create(want.Name, os.FileMode(want.Mode))
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(out, h), io.LimitReader(contextReader{ctx, r}, want.Size+1))
		e = errors.Join(e, out.Sync(), out.Close())
		if e != nil {
			return e
		}
		if n != want.Size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return errors.New("member content changed")
		}
		return nil
	}
	if a.OS == "windows" {
		z, e := zip.NewReader(f, a.Size)
		if e != nil {
			return e
		}
		if len(z.File) != len(a.Members) {
			return errors.New("member count changed")
		}
		for i, entry := range z.File {
			r, e := entry.Open()
			if e != nil {
				return e
			}
			e = errors.Join(consume(i, entry.Name, int64(entry.UncompressedSize64), entry.Mode(), r), r.Close())
			if e != nil {
				return e
			}
		}
	} else {
		g, e := gzip.NewReader(io.NewSectionReader(f, 0, a.Size))
		if e != nil {
			return e
		}
		defer g.Close()
		g.Multistream(false)
		r := tar.NewReader(contextReader{ctx, g})
		count := 0
		for {
			entry, e := r.Next()
			if errors.Is(e, io.EOF) {
				break
			}
			if e != nil {
				return e
			}
			if entry.Typeflag != tar.TypeReg || entry.Linkname != "" || entry.PAXRecords != nil || entry.Format != tar.FormatUSTAR {
				return errors.New("member type changed")
			}
			if e = consume(count, entry.Name, entry.Size, entry.FileInfo().Mode(), r); e != nil {
				return e
			}
			count++
		}
		if count != len(a.Members) {
			return errors.New("member count changed")
		}
	}
	return ctx.Err()
}
