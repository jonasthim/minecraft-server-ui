package candidate

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// VerifyBundle checks a previously authenticated Bundle, including the complete
// archive digest and the exact platform allowlist. It writes only a bounded
// private temporary file, never archive paths or extracted privileged assets.
func VerifyBundle(ctx context.Context, a Bundle, source io.Reader) (err error) {
	if err = validateBundle(a); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "minecraft-ui-bundle-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close(), os.Remove(f.Name())) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, source}, a.Size+1))
	if err != nil {
		return err
	}
	if n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("bootstrap bundle size or SHA256 mismatch")
	}
	consume := func(index int, name string, size int64, mode os.FileMode, r io.Reader) error {
		if index >= len(a.Members) {
			return errors.New("extra bootstrap archive member")
		}
		want := a.Members[index]
		if name != want.Name || size != want.Size || !mode.IsRegular() || mode.Perm() != os.FileMode(want.Mode) || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return errors.New("unsafe, reordered or unexpected bootstrap member")
		}
		h := sha256.New()
		n, e := io.Copy(h, io.LimitReader(contextReader{ctx, r}, size+1))
		if e != nil {
			return e
		}
		if n != size || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return fmt.Errorf("bootstrap member %s size or SHA256 mismatch", name)
		}
		return nil
	}
	if a.OS == "windows" {
		offset, e := zipDirectory(f, a.Size, len(a.Members))
		if e != nil {
			return e
		}
		z, e := zip.NewReader(f, a.Size)
		if e != nil {
			return e
		}
		if len(z.File) != len(a.Members) {
			return errors.New("bootstrap ZIP member count mismatch")
		}
		var next int64
		for i, entry := range z.File {
			if entry.Method != zip.Deflate || entry.Flags != 8 || len(entry.Extra) != 0 || entry.Comment != "" || entry.UncompressedSize64 > uint64(MaxBinaryBytes) {
				return errors.New("unsupported bootstrap ZIP member encoding")
			}
			dataOffset, e := entry.DataOffset()
			if e != nil {
				return e
			}
			if dataOffset != next+30+int64(len(entry.Name)) {
				return errors.New("bootstrap ZIP contains unlisted local data")
			}
			var local [30]byte
			if _, e = f.ReadAt(local[:], next); e != nil {
				return e
			}
			if binary.LittleEndian.Uint32(local[:4]) != 0x04034b50 || binary.LittleEndian.Uint16(local[26:28]) != uint16(len(entry.Name)) || binary.LittleEndian.Uint16(local[28:30]) != 0 {
				return errors.New("invalid bootstrap ZIP local header")
			}
			localName := make([]byte, len(entry.Name))
			if _, e = f.ReadAt(localName, next+30); e != nil {
				return e
			}
			if !bytes.Equal(localName, []byte(entry.Name)) || binary.LittleEndian.Uint16(local[6:8]) != entry.Flags || binary.LittleEndian.Uint16(local[8:10]) != entry.Method {
				return errors.New("bootstrap ZIP local identity differs from central directory")
			}
			r, e := entry.Open()
			if e != nil {
				return e
			}
			e = errors.Join(consume(i, entry.Name, int64(entry.UncompressedSize64), entry.Mode(), r), r.Close())
			if e != nil {
				return e
			}
			descriptorOffset := dataOffset + int64(entry.CompressedSize64)
			var descriptor [16]byte
			if _, e = f.ReadAt(descriptor[:], descriptorOffset); e != nil {
				return e
			}
			u32 := binary.LittleEndian.Uint32
			if u32(descriptor[:4]) != 0x08074b50 || u32(descriptor[4:8]) != entry.CRC32 || uint64(u32(descriptor[8:12])) != entry.CompressedSize64 || uint64(u32(descriptor[12:16])) != entry.UncompressedSize64 {
				return errors.New("invalid bootstrap ZIP data descriptor")
			}
			next = descriptorOffset + 16
		}
		if next != offset {
			return errors.New("bootstrap ZIP contains hidden local payload")
		}
	} else {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		br := bufio.NewReader(contextReader{ctx, f})
		g, e := gzip.NewReader(br)
		if e != nil {
			return e
		}
		defer func() { err = errors.Join(err, g.Close()) }()
		g.Multistream(false)
		var total int64 = 1 << 20
		for _, m := range a.Members {
			total += m.Size
		}
		limited := io.LimitReader(contextReader{ctx, g}, total)
		r := tar.NewReader(limited)
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
				return errors.New("unsafe bootstrap tar member")
			}
			if e = consume(count, entry.Name, entry.Size, entry.FileInfo().Mode(), r); e != nil {
				return e
			}
			count++
		}
		if count != len(a.Members) {
			return errors.New("bootstrap tar member count mismatch")
		}
		tail, e := io.ReadAll(io.LimitReader(contextReader{ctx, g}, (1<<20)+1))
		if e != nil {
			return e
		}
		if len(tail) > 1<<20 {
			return errors.New("bootstrap gzip trailing data exceeds bound")
		}
		for _, b := range tail {
			if b != 0 {
				return errors.New("bootstrap gzip contains trailing payload")
			}
		}
		if _, e := br.ReadByte(); !errors.Is(e, io.EOF) {
			return errors.New("bootstrap contains data after gzip stream")
		}

	}
	return ctx.Err()
}

func zipDirectory(f *os.File, size int64, count int) (int64, error) {
	if size < 22 {
		return 0, errors.New("truncated bootstrap ZIP")
	}
	var end [22]byte
	if _, e := f.ReadAt(end[:], size-22); e != nil {
		return 0, e
	}
	u16, u32 := binary.LittleEndian.Uint16, binary.LittleEndian.Uint32
	if u32(end[:4]) != 0x06054b50 || u16(end[4:6]) != 0 || u16(end[6:8]) != 0 || int(u16(end[8:10])) != count || int(u16(end[10:12])) != count || u16(end[20:22]) != 0 {
		return 0, errors.New("bootstrap ZIP must have one bounded exact-count directory")
	}
	length, offset := int64(u32(end[12:16])), int64(u32(end[16:20]))
	if length < int64(count*46) || length > 16<<10 || offset+length != size-22 {
		return 0, errors.New("bootstrap ZIP directory bounds mismatch")
	}
	return offset, nil
}
