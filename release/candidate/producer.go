package candidate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/pe"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Config struct {
	SourceRoot, Output, Version, Repository                    string
	Schema                                                     int64
	PublishedAt                                                time.Time
	LinuxManager, WindowsManager, LinuxUpdater, WindowsUpdater string
	KeyID                                                      string
	SigningKey                                                 ed25519.PrivateKey
	Development                                                bool
}
type input struct {
	Member
	path string
}
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
func regular(path string) (*os.File, error) {
	absolute, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	for p := absolute; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil {
			return nil, e
		}
		if s.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("source or parent is a symlink")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	info, e := os.Lstat(absolute)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source is not a regular file")
	}
	f, e := os.Open(absolute)
	if e != nil {
		return nil, e
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		_ = f.Close()
		return nil, errors.New("source binding changed")
	}
	return f, nil
}
func snapshot(ctx context.Context, source, destination, name string, mode uint32, limit int64) (out input, err error) {
	if err = ctx.Err(); err != nil {
		return out, err
	}
	f, err := regular(source)
	if err != nil {
		return out, fmt.Errorf("open source %s: %w", name, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	before, err := f.Stat()
	if err != nil {
		return out, err
	}
	if before.Size() < 1 || before.Size() > limit {
		return out, fmt.Errorf("source %s exceeds size bounds", name)
	}
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(contextReader{ctx, f}, limit+1))
	if err != nil {
		return out, err
	}
	after, err := f.Stat()
	if err != nil {
		return out, err
	}
	if n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return out, errors.New("source changed during snapshot")
	}
	if err = dst.Sync(); err != nil {
		return out, err
	}
	return input{Member: Member{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil)), Mode: mode}, path: destination}, ctx.Err()
}
func validateExecutable(path, platform, command string) error {
	info, e := buildinfo.ReadFile(path)
	if e != nil {
		return errors.New("binary is not a stamped Go executable")
	}
	if info.Path != "github.com/jonasthim/minecraft-server-ui/cmd/"+command {
		return errors.New("binary has wrong command identity")
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != platform || settings["GOARCH"] != "amd64" {
		return errors.New("binary has wrong OS or architecture")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if platform == "linux" {
		binary, e := elf.NewFile(f)
		if e != nil {
			return e
		}
		if binary.Machine != elf.EM_X86_64 || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) {
			return errors.New("binary is not an x64 Linux executable")
		}
	} else {
		binary, e := pe.NewFile(f)
		if e != nil {
			return e
		}
		if binary.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || binary.Characteristics&pe.IMAGE_FILE_DLL != 0 || binary.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 {
			return errors.New("binary is not an x64 Windows executable")
		}
	}
	return nil
}
func writeArchive(ctx context.Context, path, platform string, files []input) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	copyFile := func(w io.Writer, a input) error {
		source, e := os.Open(a.path)
		if e != nil {
			return e
		}
		n, copyErr := io.Copy(w, contextReader{ctx, source})
		return errors.Join(copyErr, source.Close(), func() error {
			if n != a.Size {
				return errors.New("snapshot size changed")
			}
			return nil
		}())
	}
	if platform == "linux" {
		g := gzip.NewWriter(f)
		g.Header.OS = 255
		w := tar.NewWriter(g)
		for _, a := range files {
			if e := w.WriteHeader(&tar.Header{Name: a.Name, Mode: int64(a.Mode), Typeflag: tar.TypeReg, Size: a.Size, Format: tar.FormatUSTAR}); e != nil {
				_ = w.Close()
				_ = g.Close()
				return e
			}
			if e := copyFile(w, a); e != nil {
				_ = w.Close()
				_ = g.Close()
				return e
			}
		}
		if err = errors.Join(w.Close(), g.Close()); err != nil {
			return err
		}
	} else {
		w := zip.NewWriter(f)
		for _, a := range files {
			h := &zip.FileHeader{Name: a.Name, Method: zip.Deflate}
			h.SetMode(os.FileMode(a.Mode))
			target, e := w.CreateHeader(h)
			if e != nil {
				_ = w.Close()
				return e
			}
			if e = copyFile(target, a); e != nil {
				_ = w.Close()
				return e
			}
		}
		if err = w.Close(); err != nil {
			return err
		}
	}
	if err = f.Sync(); err != nil {
		return err
	}
	return ctx.Err()
}
func fileDigest(ctx context.Context, path string, limit int64) (int64, string, error) {
	f, e := os.Open(path)
	if e != nil {
		return 0, "", e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(contextReader{ctx, f}, limit+1))
	if e != nil {
		return 0, "", e
	}
	if n < 1 || n > limit {
		return 0, "", errors.New("archive exceeds bounds")
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
func writeBytes(path string, body []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(body); err != nil {
		return err
	}
	return f.Sync()
}

// Produce refuses existing output directories, snapshots all sources, and only
// publishes after validation. Consumers must wait for successful process exit.
// A publication error removes the newly owned directory and reports any cleanup
// failure. It never replaces an existing candidate or emits a private key.
func Produce(ctx context.Context, c Config) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if !validVersion(c.Version) || !repositoryRE.MatchString(c.Repository) || strings.Contains(c.Repository, "..") || c.Schema < 1 || !keyRE.MatchString(c.KeyID) || c.PublishedAt.IsZero() || c.PublishedAt.Nanosecond() != 0 || c.PublishedAt.Location() != time.UTC {
		return errors.New("invalid version, repository, schema, key identity or whole-second UTC publication time")
	}
	if len(c.SigningKey) != ed25519.PrivateKeySize || !bytes.Equal(ed25519.NewKeyFromSeed(c.SigningKey[:ed25519.SeedSize]), c.SigningKey) {
		return errors.New("a valid supplied Ed25519 signing key is required")
	}
	output, e := filepath.Abs(c.Output)
	if e != nil || c.Output == "" {
		return errors.New("new output directory is required")
	}
	if _, e = os.Lstat(output); !errors.Is(e, os.ErrNotExist) {
		return errors.New("output already exists or cannot be inspected")
	}
	parent := filepath.Dir(output)
	if s, e := os.Stat(parent); e != nil || !s.IsDir() {
		return errors.New("output parent directory must already exist")
	}
	work, e := os.MkdirTemp(parent, ".candidate-")
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	scratch := filepath.Join(work, "inputs")
	if e = os.Mkdir(scratch, 0700); e != nil {
		return e
	}
	products := filepath.Join(work, "products")
	if e = os.Mkdir(products, 0700); e != nil {
		return e
	}
	now := c.PublishedAt
	update := Update{Format: 1, Repository: c.Repository, Version: c.Version, PublishedAt: now, ExpiresAt: now.Add(validity), SchemaBefore: c.Schema, SchemaAfter: c.Schema}
	trustLabel := "OWNER SIGNED"
	if c.Development {
		trustLabel = "DEVELOPMENT ONLY"
	}
	bootstrap := Bootstrap{Format: 1, Purpose: "minecraft-ui-bootstrap", Repository: c.Repository, Version: c.Version, PublishedAt: now, ExpiresAt: now.Add(validity), Schema: c.Schema, Trust: trustLabel}
	for _, platform := range []string{"linux", "windows"} {
		manager, updater := c.LinuxManager, c.LinuxUpdater
		if platform == "windows" {
			manager, updater = c.WindowsManager, c.WindowsUpdater
		}
		var files []input
		for i, a := range members(platform) {
			source := filepath.Join(c.SourceRoot, "deploy", platform, a.name)
			limit := MaxAssetBytes
			switch i {
			case 0:
				source = manager
				limit = MaxBinaryBytes
			case 1:
				source = updater
				limit = MaxBinaryBytes
			case 2, 3:
				source = filepath.Join(c.SourceRoot, a.name)
			}
			in, e := snapshot(ctx, source, filepath.Join(scratch, platform+"-"+a.name), a.name, a.mode, limit)
			if e != nil {
				return e
			}
			if i < 2 {
				command := "minecraft-ui"
				if i == 1 {
					command = "minecraft-ui-update"
				}
				if e = validateExecutable(in.path, platform, command); e != nil {
					return fmt.Errorf("%s %s: %w", platform, command, e)
				}
				receiptSchema := c.Schema
				if i == 1 {
					receiptSchema = 0
				}
				if e = verifyReceipt(ctx, source, in, platform, command, c.Version, receiptSchema); e != nil {
					return e
				}
			}
			files = append(files, in)
		}
		name := archiveName(platform, false)
		if e = writeArchive(ctx, filepath.Join(products, name), platform, []input{files[0], files[2], files[3]}); e != nil {
			return e
		}
		size, hash, e := fileDigest(ctx, filepath.Join(products, name), MaxBinaryBytes)
		if e != nil {
			return e
		}
		update.Artifacts = append(update.Artifacts, UpdateArtifact{OS: platform, Arch: "amd64", Name: name, Size: size, SHA256: hash, BinarySize: files[0].Size, BinarySHA256: files[0].SHA256})
		name = archiveName(platform, true)
		if e = writeArchive(ctx, filepath.Join(products, name), platform, files); e != nil {
			return e
		}
		size, hash, e = fileDigest(ctx, filepath.Join(products, name), MaxBundleBytes)
		if e != nil {
			return e
		}
		a := Bundle{OS: platform, Arch: "amd64", Name: name, Size: size, SHA256: hash}
		for _, file := range files {
			a.Members = append(a.Members, file.Member)
		}
		bootstrap.Artifacts = append(bootstrap.Artifacts, a)
	}
	for _, metadata := range []struct {
		name  string
		value any
	}{{"release", update}, {"bootstrap", bootstrap}} {
		body, e := canonical(metadata.value)
		if e != nil {
			return e
		}
		if len(body) > MaxBootstrapBytes {
			return errors.New("metadata exceeds bound")
		}
		sig, e := sign(body, c.KeyID, c.SigningKey)
		if e != nil {
			return e
		}
		if metadata.name == "bootstrap" {
			if _, e = VerifyBootstrap(Trust{Repository: c.Repository, PublicKeys: map[string]ed25519.PublicKey{c.KeyID: c.SigningKey.Public().(ed25519.PublicKey)}}, body, sig, c.Version, now); e != nil {
				return e
			}
		}
		if e = writeBytes(filepath.Join(products, metadata.name+".json"), body); e != nil {
			return e
		}
		if e = writeBytes(filepath.Join(products, metadata.name+".sig"), sig); e != nil {
			return e
		}
	}
	if c.Development {
		body := []byte("DEVELOPMENT ONLY\nEphemeral test trust; these candidates are not qualified stable releases.\nNever install this public key as production update trust. No private key is retained.\n")
		if e = writeBytes(filepath.Join(products, "DEVELOPMENT-ONLY.txt"), body); e != nil {
			return e
		}
		trust := struct {
			Format     int               `json:"format"`
			Repository string            `json:"repository"`
			PublicKeys map[string]string `json:"public_keys"`
		}{1, c.Repository, map[string]string{c.KeyID: base64.StdEncoding.EncodeToString(c.SigningKey.Public().(ed25519.PublicKey))}}
		body, e = canonical(trust)
		if e != nil {
			return e
		}
		if e = writeBytes(filepath.Join(products, "development-trust.json"), body); e != nil {
			return e
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = os.Mkdir(output, 0755); e != nil {
		return fmt.Errorf("reserve new candidate output: %w", e)
	}
	published := false
	defer func() {
		if !published {
			err = errors.Join(err, os.RemoveAll(output))
		}
	}()
	entries, e := os.ReadDir(products)
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = os.Rename(filepath.Join(products, entry.Name()), filepath.Join(output, entry.Name())); e != nil {
			return e
		}
	}
	if runtime.GOOS != "windows" {
		directory, e := os.Open(output)
		if e != nil {
			return e
		}
		if e = errors.Join(directory.Sync(), directory.Close()); e != nil {
			return e
		}
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	published = true
	return nil
}
