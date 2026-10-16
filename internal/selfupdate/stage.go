package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// Stager requires an existing private canonical directory. An exclusive file
// prevents concurrent staging even across Service instances; stale locks fail
// closed for administrator repair. Stages are untrusted input to native helpers.
type Stager struct {
	Dir            string
	Trust          Trust
	CurrentVersion string
	Schema         int64
	OS, Arch       string
	Now            func() time.Time
	BeforePublish  func(context.Context) error
}
type Stage struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	Platform     string `json:"platform"`
	BinarySHA256 string `json:"binary_sha256"`
	Schema       int64  `json:"schema"`
}

func (s *Stager) Stage(ctx context.Context, tag string, body, envelope []byte, archive io.Reader) (_ Stage, err error) {
	var result Stage
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	manifest, err := VerifyManifest(s.Trust, body, envelope, tag, s.CurrentVersion, s.Schema, now)
	if err != nil {
		return result, err
	}
	artifact, err := manifest.Artifact(s.OS, s.Arch)
	if err != nil {
		return result, err
	}
	root, err := safepath.OpenRoot(ctx, s.Dir)
	if err != nil {
		return result, fmt.Errorf("open staging directory: %w", err)
	}
	defer root.Close()
	lock, err := root.OpenFile(".staging.lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, fmt.Errorf("staging is locked or unavailable: %w", err)
	}
	defer func() { err = errors.Join(err, lock.Close(), root.Remove(".staging.lock")) }()
	directory, err := root.Open(".")
	if err != nil {
		return result, err
	}
	entries, readErr := directory.ReadDir(5)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return result, readErr
	}
	if closeErr != nil {
		return result, closeErr
	}
	// The lock counts as one entry. At most two immutable stages are retained;
	// cleanup of stages is an explicit administrative action until activation owns it.
	if len(entries) > 2 {
		return result, errors.New("staging capacity reached; administrator cleanup is required")
	}
	idHash := sha256.Sum256(append(append(append([]byte{}, body...), 0), []byte(s.OS+"/"+s.Arch)...))
	id := hex.EncodeToString(idHash[:])
	if _, err := root.Lstat(id); err == nil {
		return result, errors.New("release is already staged")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return result, err
	}
	temporary := ".pending-" + hex.EncodeToString(nonce)
	if err := root.Mkdir(temporary, 0700); err != nil {
		return result, fmt.Errorf("create staging transaction: %w", err)
	}
	defer func() { err = errors.Join(err, root.RemoveAll(temporary)) }()
	stage, err := root.OpenRoot(temporary)
	if err != nil {
		return result, err
	}
	defer stage.Close()
	if err := writeFile(ctx, stage, "release.json", bytes.NewReader(body), int64(len(body)), ""); err != nil {
		return result, err
	}
	if err := writeFile(ctx, stage, "release.sig", bytes.NewReader(envelope), int64(len(envelope)), ""); err != nil {
		return result, err
	}
	if err := writeFile(ctx, stage, artifact.Name, archive, artifact.Size, artifact.SHA256); err != nil {
		return result, err
	}
	f, err := stage.Open(artifact.Name)
	if err != nil {
		return result, err
	}
	extractErr := extract(ctx, stage, f, artifact)
	err = errors.Join(extractErr, f.Close())
	if err != nil {
		return result, err
	}
	result = Stage{ID: id, Version: manifest.Version, Platform: s.OS + "/" + s.Arch, BinarySHA256: artifact.BinarySHA256, Schema: s.Schema}
	ready, err := json.Marshal(result)
	if err != nil {
		return Stage{}, err
	}
	if err := writeFile(ctx, stage, "ready.json", bytes.NewReader(ready), int64(len(ready)), ""); err != nil {
		return Stage{}, err
	}
	if err := syncRoot(stage); err != nil {
		return Stage{}, err
	}
	// Compare the original absolute binding before and after publication. Retained
	// handles prevent traversal; this fence prevents success for a detached tree.
	if err := sameRoot(ctx, s.Dir, root); err != nil {
		return Stage{}, err
	}
	publicationNow := time.Now()
	if s.Now != nil {
		publicationNow = s.Now()
	}
	if _, err := VerifyManifest(s.Trust, body, envelope, tag, s.CurrentVersion, s.Schema, publicationNow); err != nil {
		return Stage{}, err
	}
	if s.BeforePublish != nil {
		if err := s.BeforePublish(ctx); err != nil {
			return Stage{}, err
		}
	}
	if err := root.Rename(temporary, id); err != nil {
		return Stage{}, fmt.Errorf("publish verified release: %w", err)
	}
	if err := syncRoot(root); err != nil {
		return Stage{}, fmt.Errorf("sync staged release publication (stage may exist): %w", err)
	}
	if err := sameRoot(ctx, s.Dir, root); err != nil {
		return Stage{}, err
	}
	return result, nil
}
func sameRoot(ctx context.Context, path string, root *os.Root) error {
	check, err := safepath.OpenRoot(ctx, path)
	if err != nil {
		return err
	}
	defer check.Close()
	a, err := root.Stat(".")
	if err != nil {
		return err
	}
	b, err := check.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) {
		return errors.New("staging directory binding changed")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func writeFile(ctx context.Context, root *os.Root, name string, source io.Reader, size int64, wantHash string) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("create staged file: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(contextReader{ctx, source}, size+1))
	if err != nil {
		return fmt.Errorf("write staged file: %w", err)
	}
	if n != size {
		return errors.New("release file size mismatch")
	}
	if wantHash != "" && hex.EncodeToString(hash.Sum(nil)) != wantHash {
		return errors.New("release file SHA256 mismatch")
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync staged file: %w", err)
	}
	return ctx.Err()
}
func extract(ctx context.Context, root *os.Root, f *os.File, a Artifact) error {
	seen := map[string]bool{}
	consume := func(name string, size int64, mode os.FileMode, reader io.Reader) error {
		if seen[name] || !mode.IsRegular() || size < 1 {
			return errors.New("unsafe or duplicate release archive member")
		}
		seen[name] = true
		switch name {
		case binaryName(a.OS):
			if size != a.BinarySize {
				return errors.New("release binary size mismatch")
			}
			return writeFile(ctx, root, name, reader, size, a.BinarySHA256)
		case "LICENSE", "THIRD_PARTY_NOTICES.txt":
			if size > 1<<20 {
				return errors.New("release notices exceed limit")
			}
			return writeFile(ctx, root, name, reader, size, "")
		default:
			return errors.New("unexpected release archive member")
		}
	}
	if a.OS == "windows" {
		if err := validateZipDirectory(f, a.Size); err != nil {
			return err
		}
		z, err := zip.NewReader(f, a.Size)
		if err != nil {
			return fmt.Errorf("open release zip: %w", err)
		}
		if len(z.File) != 3 {
			return errors.New("release archive must contain executable and license notices")
		}
		for _, entry := range z.File {
			if entry.UncompressedSize64 > uint64(MaxArtifactBytes) {
				return errors.New("oversized release zip member")
			}
			r, err := entry.Open()
			if err != nil {
				return err
			}
			e := consume(entry.Name, int64(entry.UncompressedSize64), entry.Mode(), r)
			if e == nil {
				var one [1]byte
				n, end := r.Read(one[:])
				if n != 0 || !errors.Is(end, io.EOF) {
					e = errors.New("release zip checksum/trailer mismatch")
				}
			}
			err = errors.Join(e, r.Close())
			if err != nil {
				return err
			}
		}
	} else {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("open release gzip: %w", err)
		}
		defer gz.Close()
		limited := io.LimitReader(contextReader{ctx, gz}, a.BinarySize+(2<<20)+(1<<20))
		tr := tar.NewReader(limited)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("read release tar: %w", err)
			}
			if len(seen) >= 3 || hdr.Typeflag != tar.TypeReg || hdr.PAXRecords != nil {
				return errors.New("unsafe release tar entry")
			}
			if err := consume(hdr.Name, hdr.Size, hdr.FileInfo().Mode(), tr); err != nil {
				return err
			}
		}
		// Drain through the gzip checksum; reject hidden second archives/nonzero data
		// and bound decompression even when the tar parser returns EOF early.
		trailing, err := io.ReadAll(io.LimitReader(contextReader{ctx, gz}, (1<<20)+1))
		if err != nil {
			return fmt.Errorf("verify release gzip trailer: %w", err)
		}
		if len(trailing) > 1<<20 {
			return errors.New("release gzip trailing data exceeds bound")
		}
		for _, b := range trailing {
			if b != 0 {
				return errors.New("release gzip contains trailing data")
			}
		}
	}
	if len(seen) != 3 || !seen[binaryName(a.OS)] || !seen["LICENSE"] || !seen["THIRD_PARTY_NOTICES.txt"] {
		return errors.New("release archive is missing executable or license notices")
	}
	return nil
}

// StageDirectory returns a fixed child only after validation; callers must still
// use checked roots and privileged re-verification before activation.
func StageDirectory(parent, id string) (string, error) {
	if !validHash(id) {
		return "", errors.New("invalid release stage identifier")
	}
	return filepath.Join(parent, id), nil
}

// The standard ZIP reader consumes central headers until a bad signature and
// only compares the entry count modulo65536. Preflight the fixed release format
// so it cannot allocate a directory proportional to the whole artifact first.
// Release ZIPs deliberately exclude ZIP64, comments and trailing payloads.
func validateZipDirectory(file *os.File, size int64) error {
	if size < 22 {
		return errors.New("release ZIP is truncated")
	}
	var end [22]byte
	if _, err := file.ReadAt(end[:], size-22); err != nil {
		return err
	}
	u16 := binary.LittleEndian.Uint16
	u32 := binary.LittleEndian.Uint32
	if u32(end[:4]) != 0x06054b50 || u16(end[4:6]) != 0 || u16(end[6:8]) != 0 || u16(end[8:10]) != 3 || u16(end[10:12]) != 3 || u16(end[20:22]) != 0 {
		return errors.New("release ZIP must have a single bounded three-entry directory")
	}
	directorySize := int64(u32(end[12:16]))
	offset := int64(u32(end[16:20]))
	if directorySize < 3*46 || directorySize > 16<<10 || offset+directorySize != size-22 {
		return errors.New("release ZIP central directory is oversized or inconsistent")
	}
	return nil
}
