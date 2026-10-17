// Package outer renders release-specific, independently reviewed prepare-only
// entry scripts. It never executes inputs, signs releases, or installs services.
package outer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

//go:embed templates/*
var templates embed.FS

type Config struct {
	Repository, Version, LinuxPreparer, WindowsPreparer, TrustFile, Output string
	Development                                                            bool
}

type Asset struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Entry struct {
	Format          int     `json:"format"`
	Repository      string  `json:"repository"`
	PreparerVersion string  `json:"preparer_version"`
	TrustFileSHA256 string  `json:"trust_file_sha256"`
	Development     bool    `json:"development"`
	Assets          []Asset `json:"assets"`
}

var repositoryRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
var versionRE = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func validVersion(s string) bool {
	if !versionRE.MatchString(s) {
		return false
	}
	for _, n := range strings.Split(s[1:], ".") {
		if _, e := strconv.ParseUint(n, 10, 64); e != nil {
			return false
		}
	}
	return true
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// checkAncestry refuses symlinks, including dangling links. The caller must
// supply a trusted private parent: this producer is not a privilege boundary.
func checkAncestry(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || s.Mode()&os.ModeSymlink != 0 {
			return errors.New("path ancestry must exist without symlinks")
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}

func readTrust(ctx context.Context, path string) ([]byte, error) {
	p, e := filepath.Abs(path)
	if e != nil || path == "" {
		return nil, errors.New("public trust file is required")
	}
	if e = checkAncestry(p); e != nil {
		return nil, e
	}
	before, e := os.Lstat(p)
	if e != nil || !before.Mode().IsRegular() {
		return nil, errors.New("public trust must be a regular file")
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, errors.New("public trust unavailable")
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !os.SameFile(before, s) {
		return nil, errors.New("public trust identity changed")
	}
	body, e := io.ReadAll(io.LimitReader(f, 16<<10+1))
	if e != nil {
		return nil, errors.New("public trust read failed")
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	after, e := f.Stat()
	if e != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || int64(len(body)) != before.Size() {
		return nil, errors.New("public trust changed or exceeds bounds")
	}
	return body, nil
}

// Produce creates a new directory under a caller-controlled private parent.
// Consume it only after success. Output is deterministic; receipts remain
// unsigned provenance, not independent proof of native execution. No input runs.
func Produce(ctx context.Context, c Config) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if !repositoryRE.MatchString(c.Repository) || strings.Contains(c.Repository, "..") || !validVersion(c.Version) {
		return errors.New("exact stable preparer version and repository required")
	}
	if c.Output == "" || !filepath.IsAbs(c.Output) || filepath.Clean(c.Output) != c.Output {
		return errors.New("new clean absolute output directory required")
	}
	if _, e := os.Lstat(c.Output); !errors.Is(e, os.ErrNotExist) {
		return errors.New("output already exists or cannot be inspected")
	}
	parent := filepath.Dir(c.Output)
	if err = checkAncestry(parent); err != nil {
		return err
	}
	trust, e := readTrust(ctx, c.TrustFile)
	if e != nil {
		return e
	}
	if _, e = candidate.ParsePublicTrust(trust, c.Repository); e != nil {
		return e
	}
	work, e := os.MkdirTemp(parent, ".outer-")
	if e != nil {
		return errors.New("private snapshot directory unavailable")
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()
	entry := Entry{Format: 1, Repository: c.Repository, PreparerVersion: c.Version, TrustFileSHA256: digest(trust), Development: c.Development}
	for _, platform := range []string{"linux", "windows"} {
		name, source := "minecraft-ui-bootstrap-linux-amd64", c.LinuxPreparer
		if platform == "windows" {
			name, source = "minecraft-ui-bootstrap-windows-amd64.exe", c.WindowsPreparer
		}
		member, e := candidate.SnapshotNativeInput(ctx, source, filepath.Join(work, name), platform, "minecraft-ui-bootstrap", c.Version, 0)
		if e != nil {
			return fmt.Errorf("%s preparer refused: %w", platform, e)
		}
		entry.Assets = append(entry.Assets, Asset{name, member.Size, member.SHA256})
	}
	for i, spec := range []struct{ name, tmpl string }{{"minecraft-ui-prepare.sh", "linux.sh.tmpl"}, {"minecraft-ui-prepare.ps1", "windows.ps1.tmpl"}} {
		data := struct {
			Config
			Binary                    Asset
			TrustLiteral, TrustBase64 string
		}{c, entry.Assets[i], "'" + strings.ReplaceAll(string(trust), "'", "'\"'\"'") + "'", base64.StdEncoding.EncodeToString(trust)}
		t, e := template.ParseFS(templates, "templates/"+spec.tmpl)
		if e != nil {
			return e
		}
		var b bytes.Buffer
		if e = t.Execute(&b, data); e != nil {
			return e
		}
		if b.Len() > 64<<10 {
			return errors.New("entry script exceeds bounds")
		}
		if e = os.WriteFile(filepath.Join(work, spec.name), b.Bytes(), 0600); e != nil {
			return e
		}
		entry.Assets = append(entry.Assets, Asset{spec.name, int64(b.Len()), digest(b.Bytes())})
	}
	body, e := json.Marshal(entry)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(work, "bootstrap-entry.json"), append(body, '\n'), 0600); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	// Exclusive publication preserves existing files and dangling links. Holding
	// roots binds writes; cleanup refuses an externally replaced output identity.
	p, e := os.OpenRoot(parent)
	if e != nil {
		return e
	}
	defer p.Close()
	base := filepath.Base(c.Output)
	if e = p.Mkdir(base, 0700); e != nil {
		return errors.New("output publication refused")
	}
	owned, e := p.Stat(base)
	if e != nil {
		return e
	}
	defer func() {
		if err != nil {
			current, e := p.Lstat(base)
			if e == nil && os.SameFile(owned, current) {
				err = errors.Join(err, p.RemoveAll(base))
			} else {
				err = errors.Join(err, errors.New("output identity changed; cleanup refused"))
			}
		}
	}()
	out, e := p.OpenRoot(base)
	if e != nil {
		return e
	}
	defer out.Close()
	names := []string{"minecraft-ui-bootstrap-linux-amd64", "minecraft-ui-bootstrap-windows-amd64.exe", "minecraft-ui-prepare.sh", "minecraft-ui-prepare.ps1", "bootstrap-entry.json"}
	for _, name := range names {
		if e = ctx.Err(); e != nil {
			return e
		}
		b, e := os.ReadFile(filepath.Join(work, name))
		if e != nil {
			return e
		}
		mode := os.FileMode(0644)
		if name == "minecraft-ui-bootstrap-linux-amd64" || name == "minecraft-ui-bootstrap-windows-amd64.exe" || name == "minecraft-ui-prepare.sh" {
			mode = 0755
		}
		f, e := out.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if e != nil {
			return e
		}
		_, writeErr := f.Write(b)
		e = errors.Join(writeErr, f.Sync(), f.Close())
		if e != nil {
			return e
		}
	}
	current, e := p.Lstat(base)
	if e != nil || !os.SameFile(owned, current) {
		return errors.New("output identity changed")
	}
	return nil
}
