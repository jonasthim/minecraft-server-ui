// Package bootstrap prepares independently authenticated installation inputs.
// It never executes inputs, installs services, or alters native trust.
package bootstrap

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

const ReceiptName = "completion.json"
const MetadataName = "bootstrap.json"
const SignatureName = "bootstrap.sig"
const MaxDuration = 10 * time.Minute

// Acquisition is injectable for deterministic tests. The shipping command uses
// nil, which selects Source's fixed public GitHub transport or local artifacts.
type Acquisition interface {
	Latest(context.Context) (*selfupdate.ReleaseSummary, error)
	BootstrapMetadata(context.Context, string) ([]byte, []byte, error)
	BootstrapArchive(context.Context, string, string) (io.ReadCloser, error)
}

type Options struct {
	Repository       string
	Version          string
	TrustFile        string
	Output           string
	Artifacts        string
	AllowDevelopment bool
	// Platform and clock injection belong only to library tests. The command
	// always uses its native runtime platform and the real UTC clock.
	OS   string
	Arch string
	Now  func() time.Time
}

type Receipt struct {
	Format         int                `json:"format"`
	Repository     string             `json:"repository"`
	Version        string             `json:"version"`
	Schema         int64              `json:"schema"`
	OS             string             `json:"os"`
	Arch           string             `json:"arch"`
	Trust          string             `json:"trust"`
	ManifestSHA256 string             `json:"manifest_sha256"`
	Members        []candidate.Member `json:"members"`
}

// Error contains only an authored stage label, never a remote response, URL,
// transport error, caller-supplied path, credential, or metadata string.
type Error struct{ Stage string }

func (e *Error) Error() string   { return "bootstrap preparation failed: " + e.Stage }
func failure(stage string) error { return &Error{Stage: stage} }

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

func validate(o Options) error {
	if !repositoryPattern.MatchString(o.Repository) || strings.Contains(o.Repository, "..") {
		return failure("invalid repository")
	}
	if o.Version != "latest" {
		if _, err := selfupdate.ParseStable(o.Version); err != nil {
			return failure("invalid version")
		}
	}
	if !absolutePath(o.TrustFile) || !absolutePath(o.Output) || filepath.Dir(o.Output) == o.Output {
		return failure("invalid absolute paths")
	}
	if o.Artifacts != "" && (!absolutePath(o.Artifacts) || o.Version == "latest") {
		return failure("offline artifacts require an absolute directory and exact version")
	}
	if (o.OS != "linux" && o.OS != "windows") || o.Arch != "amd64" {
		return failure("unsupported platform")
	}
	return nil
}

// Prepare exclusively creates Output under a checked caller-controlled parent.
// Completion is signalled by both a successful return and the receipt written
// last. Directory visibility is not atomic. Callers must wait for success, and
// reverify on any later privilege crossing. No downloaded executable is run.
func Prepare(ctx context.Context, o Options, source Acquisition) (receipt Receipt, err error) {
	if o.OS == "" {
		o.OS = runtime.GOOS
	}
	if o.Arch == "" {
		o.Arch = runtime.GOARCH
	}
	if err = validate(o); err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(ctx, MaxDuration)
	defer cancel()
	if ctx.Err() != nil {
		return receipt, failure("cancelled")
	}
	trustDir, e := openChecked(filepath.Dir(o.TrustFile))
	if e != nil {
		return receipt, failure("public trust path")
	}
	defer trustDir.close()
	public, e := readRegular(ctx, trustDir.root, filepath.Base(o.TrustFile), 16<<10)
	if e != nil || trustDir.check() != nil {
		return receipt, failure("public trust file")
	}
	trust, e := candidate.ParsePublicTrust(public, o.Repository)
	if e != nil {
		return receipt, failure("public trust validation")
	}
	var local *checkedRoot
	if o.Artifacts != "" {
		if source != nil {
			return receipt, failure("ambiguous acquisition source")
		}
		local, e = openChecked(o.Artifacts)
		if e != nil {
			return receipt, failure("offline artifact directory")
		}
		defer local.close()
		source = &localAcquisition{local}
	} else if source == nil {
		source, e = selfupdate.NewSource(selfupdate.Trust(trust))
		if e != nil {
			return receipt, failure("release source configuration")
		}
	}
	version := o.Version
	if version == "latest" {
		latest, e := source.Latest(ctx)
		if e != nil {
			return receipt, failure("latest release discovery")
		}
		if latest == nil {
			return receipt, failure("no published stable release")
		}
		if _, e = selfupdate.ParseStable(latest.Version); e != nil {
			return receipt, failure("latest release version")
		}
		version = latest.Version
	}
	body, sig, e := source.BootstrapMetadata(ctx, version)
	if e != nil {
		return receipt, failure("metadata acquisition")
	}
	body = bytes.Clone(body)
	sig = bytes.Clone(sig)
	now := time.Now().UTC()
	if o.Now != nil {
		now = o.Now().UTC()
	}
	manifest, e := candidate.VerifyBootstrap(trust, body, sig, version, now)
	if e != nil {
		return receipt, failure("metadata authentication")
	}
	if manifest.Trust == "DEVELOPMENT ONLY" && !o.AllowDevelopment {
		return receipt, failure("development metadata requires explicit opt-in")
	}
	a := manifest.Artifacts[0]
	if o.OS == "windows" {
		a = manifest.Artifacts[1]
	}
	if a.OS != o.OS || a.Arch != o.Arch {
		return receipt, failure("platform identity")
	}
	d, e := newDestination(o.Output)
	if e != nil {
		return receipt, failure("new private output")
	}
	defer d.close()
	success := false
	defer func() {
		if !success {
			if cleanupErr := d.cleanup(); cleanupErr != nil {
				err = errors.Join(err, failure("cleanup incomplete; replaced or unremovable output state retained"))
			}
		}
	}()
	if e = writePrivate(d, MetadataName, body); e != nil {
		return receipt, failure("private metadata retention")
	}
	if e = writePrivate(d, SignatureName, sig); e != nil {
		return receipt, failure("private signature retention")
	}
	r, e := source.BootstrapArchive(ctx, version, o.OS)
	if e != nil {
		return receipt, failure("archive acquisition")
	}
	if ctx.Err() != nil || verifyOwnedBytes(d, MetadataName, body) != nil || verifyOwnedBytes(d, SignatureName, sig) != nil {
		r.Close()
		return receipt, failure("authenticated input binding changed")
	}
	f, e := d.create(".bundle", 0600)
	if e != nil {
		r.Close()
		return receipt, failure("private archive snapshot")
	}
	n, e := io.Copy(f, io.LimitReader(contextReader{ctx, r}, a.Size+1))
	e = errors.Join(e, r.Close(), f.Sync())
	if e != nil || n != a.Size {
		_ = f.Close()
		return receipt, failure("archive snapshot size or read")
	}
	// Keep the creation handle alive until the read handle is bound to the same
	// identity, preventing an unlinked original's identity from being reused.
	snapshot, e := d.openOwnedRegular(".bundle", candidate.MaxBundleBytes)
	if e != nil {
		_ = f.Close()
		return receipt, failure("archive snapshot binding")
	}
	defer snapshot.Close()
	if e = f.Close(); e != nil {
		return receipt, failure("archive snapshot writer close")
	}
	if e = candidate.VerifyBundle(ctx, a, io.NewSectionReader(snapshot, 0, a.Size)); e != nil {
		return receipt, failure("archive authentication")
	}
	if e = extract(ctx, d, a, snapshot); e != nil {
		return receipt, failure("verified member extraction")
	}
	if e = d.removeOwned(".bundle"); e != nil {
		return receipt, failure("archive snapshot cleanup")
	}
	if e = snapshot.Close(); e != nil {
		return receipt, failure("archive snapshot close")
	}
	if ctx.Err() != nil {
		return receipt, failure("cancelled")
	}
	if trustDir.check() != nil || d.check() != nil || (local != nil && local.check() != nil) {
		return receipt, failure("path binding changed")
	}
	if ctx.Err() != nil || verifyOwnedBytes(d, MetadataName, body) != nil || verifyOwnedBytes(d, SignatureName, sig) != nil {
		return receipt, failure("authenticated input binding changed")
	}
	h := sha256.Sum256(body)
	receipt = Receipt{Format: 1, Repository: manifest.Repository, Version: manifest.Version, Schema: manifest.Schema, OS: a.OS, Arch: a.Arch, Trust: manifest.Trust, ManifestSHA256: hex.EncodeToString(h[:]), Members: append([]candidate.Member(nil), a.Members...)}
	encoded, e := json.Marshal(receipt)
	if e != nil {
		return Receipt{}, failure("receipt encoding")
	}
	encoded = append(encoded, '\n')
	f, e = d.create(ReceiptName, 0600)
	if e != nil {
		return Receipt{}, failure("completion receipt creation")
	}
	_, e = f.Write(encoded)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil || d.check() != nil || ctx.Err() != nil {
		return Receipt{}, failure("completion receipt publication")
	}
	success = true
	return receipt, nil
}

func writePrivate(d *destination, name string, content []byte) error {
	f, err := d.create(name, 0600)
	if err != nil {
		return err
	}
	n, writeErr := f.Write(content)
	if n != len(content) && writeErr == nil {
		writeErr = io.ErrShortWrite
	}
	writeErr = errors.Join(writeErr, f.Sync(), f.Close())
	if writeErr != nil {
		return writeErr
	}
	return verifyOwnedBytes(d, name, content)
}

func verifyOwnedBytes(d *destination, name string, expected []byte) error {
	f, err := d.openOwnedRegular(name, int64(len(expected)))
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	actual, err := io.ReadAll(io.LimitReader(f, int64(len(expected))+1))
	after, statErr := f.Stat()
	owned, ownedErr := d.owned(name)
	if err != nil || statErr != nil || ownedErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || owned.check(d.root) != nil || !bytes.Equal(actual, expected) {
		return errors.New("retained authenticated input changed")
	}
	return d.check()
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

type localAcquisition struct{ dir *checkedRoot }

func (l *localAcquisition) Latest(context.Context) (*selfupdate.ReleaseSummary, error) {
	return nil, errors.New("offline latest is forbidden")
}
func (l *localAcquisition) BootstrapMetadata(ctx context.Context, _ string) ([]byte, []byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, nil, e
	}
	b, e := readRegular(ctx, l.dir.root, "bootstrap.json", candidate.MaxBootstrapBytes)
	if e != nil {
		return nil, nil, e
	}
	s, e := readRegular(ctx, l.dir.root, "bootstrap.sig", 1024)
	return b, s, e
}
func (l *localAcquisition) BootstrapArchive(ctx context.Context, _ string, platform string) (io.ReadCloser, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	name := "minecraft-ui-bootstrap-linux-amd64.tar.gz"
	if platform == "windows" {
		name = "minecraft-ui-bootstrap-windows-amd64.zip"
	} else if platform != "linux" {
		return nil, errors.New("invalid platform")
	}
	return openRegular(l.dir.root, name, candidate.MaxBundleBytes)
}
