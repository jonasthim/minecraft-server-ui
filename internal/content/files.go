package content

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"
)

func invalid(s string) error  { return domain.E(domain.CodeValidationFailed, s) }
func conflict(s string) error { return domain.E(domain.CodeConflict, s) }
func digest(b []byte) string  { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b[:])
}
func portable(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > 160 || strings.Trim(s, " .") != s || strings.ContainsAny(s, "/\\:<>\"|?*\x00") {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	b := strings.ToUpper(strings.SplitN(s, ".", 2)[0])
	if b == "CON" || b == "PRN" || b == "AUX" || b == "NUL" || b == "CONIN$" || b == "CONOUT$" {
		return false
	}
	if len(b) == 4 && (strings.HasPrefix(b, "COM") || strings.HasPrefix(b, "LPT")) && b[3] >= '1' && b[3] <= '9' {
		return false
	}
	return true
}
func relative(s string) bool {
	if s == "" || len(s) > 900 || strings.HasPrefix(s, "/") {
		return false
	}
	for _, v := range strings.Split(s, "/") {
		if !portable(v) {
			return false
		}
	}
	return true
}
func configPath(s string) bool {
	if !relative(s) {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) > 8 || len(parts) < 2 {
		return false
	}
	if parts[0] != "config" && parts[0] != "defaultconfigs" && parts[0] != "plugins" {
		return false
	}
	if parts[0] == "plugins" && len(parts) < 3 {
		return false
	}
	for _, v := range parts {
		if blockedConfigPart(v) {
			return false
		}
	}
	switch strings.ToLower(path.Ext(s)) {
	case ".toml", ".json", ".json5", ".yaml", ".yml", ".properties", ".cfg", ".conf", ".txt":
		return true
	}
	return false
}
func blockedConfigPart(v string) bool {
	low := strings.ToLower(v)
	return strings.HasPrefix(v, ".") || low == "world" || low == "worlds" || low == "world_nether" || low == "world_the_end" || low == "runtime" || low == "runtimes" || low == "state" || low == "logs" || low == "backups" || low == "libraries" || low == "manager.db" || low == "server.properties" || low == "eula.txt" || strings.Contains(low, "rcon") || strings.Contains(low, "management")
}
func validText(b []byte) bool {
	return len(b) <= MaxConfigBytes && utf8.Valid(b) && !strings.ContainsRune(string(b), 0)
}

// parent descends through pinned root handles and rejects links before and after opening.
func parent(root *os.Root, name string, create bool) (*os.Root, string, error) {
	if !relative(name) {
		return nil, "", invalid("unsafe relative content path")
	}
	parts := strings.Split(name, "/")
	current, e := root.OpenRoot(".")
	if e != nil {
		return nil, "", e
	}
	for _, p := range parts[:len(parts)-1] {
		fi, e := current.Lstat(p)
		if os.IsNotExist(e) && create {
			if e = current.Mkdir(p, 0700); e != nil && !os.IsExist(e) {
				current.Close()
				return nil, "", e
			}
			if e = syncDir(current); e != nil {
				current.Close()
				return nil, "", e
			}
			fi, e = current.Lstat(p)
		}
		if e != nil {
			current.Close()
			return nil, "", e
		}
		if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, "", invalid("linked content directories refused")
		}
		next, e := current.OpenRoot(p)
		if e != nil {
			current.Close()
			return nil, "", e
		}
		again, e := next.Stat(".")
		if e != nil || !os.SameFile(fi, again) {
			next.Close()
			current.Close()
			return nil, "", conflict("content directory changed")
		}
		current.Close()
		current = next
	}
	return current, parts[len(parts)-1], nil
}
func readFile(ctx context.Context, root *os.Root, name string, max int64) ([]byte, error) {
	p, base, e := parent(root, name, false)
	if e != nil {
		return nil, e
	}
	defer p.Close()
	fi, e := p.Lstat(base)
	if e != nil {
		return nil, e
	}
	if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, invalid("file is linked, nonregular or too large")
	}
	f, e := p.Open(base)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(fi, opened) {
		return nil, conflict("file identity changed")
	}
	if e = singleLink(f, opened); e != nil {
		return nil, e
	}
	b, e := io.ReadAll(io.LimitReader(contextReader{ctx, f}, max+1))
	if e != nil {
		return nil, e
	}
	after, e := p.Lstat(base)
	if e != nil || !os.SameFile(opened, after) || len(b) > int(max) {
		return nil, conflict("file changed while reading")
	}
	return b, nil
}
func currentHash(ctx context.Context, r *os.Root, name string, max int64) (string, []byte, error) {
	f, e := openChecked(r, name, max)
	if os.IsNotExist(e) {
		return "", nil, nil
	}
	if e != nil {
		return "", nil, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(contextReader{ctx, f}, max+1))
	if e != nil {
		return "", nil, e
	}
	if n > max {
		return "", nil, invalid("file exceeds bound")
	}
	return hex.EncodeToString(h.Sum(nil)), nil, nil
}
func openChecked(root *os.Root, name string, max int64) (*os.File, error) {
	p, base, e := parent(root, name, false)
	if e != nil {
		return nil, e
	}
	defer p.Close()
	fi, e := p.Lstat(base)
	if e != nil {
		return nil, e
	}
	if !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, invalid("unsafe or oversized content file")
	}
	f, e := p.Open(base)
	if e != nil {
		return nil, e
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(fi, opened) {
		f.Close()
		return nil, conflict("content file identity changed")
	}
	if e = singleLink(f, opened); e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
func atomicFile(ctx context.Context, r *os.Root, name, expected string, b []byte, remove bool) error {
	return atomicStream(ctx, r, name, expected, strings.NewReader(string(b)), remove)
}
func atomicStream(ctx context.Context, r *os.Root, name, expected string, reader io.Reader, remove bool) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	p, base, e := parent(r, name, !remove)
	if e != nil {
		return e
	}
	defer p.Close()
	hash, _, e := currentHash(ctx, r, name, MaxArtifactBytes)
	if e != nil {
		return e
	}
	if hash != expected {
		return conflict("file changed since preview")
	}
	if remove {
		if expected == "" {
			return nil
		}
		if e = p.Remove(base); e != nil {
			return e
		}
		return syncDir(p)
	}
	tmp := ".content-" + newID()
	f, e := p.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer p.Remove(tmp)
	if _, e = io.Copy(f, contextReader{ctx, reader}); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	hash, _, e = currentHash(ctx, r, name, MaxArtifactBytes)
	if e != nil {
		return e
	}
	if hash != expected {
		return conflict("file changed during publication")
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	if e = p.Rename(tmp, base); e != nil {
		return fmt.Errorf("publish content: %w", e)
	}
	return syncDir(p)
}
func (s *Service) openServer(ctx context.Context, id string) (*os.Root, error) {
	d, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	return safepath.OpenRoot(ctx, d.Server)
}

func (s *Service) revalidateServer(ctx context.Context, id string, opened *os.Root) error {
	current, e := s.openServer(ctx, id)
	if e != nil {
		return e
	}
	defer current.Close()
	before, e := opened.Stat(".")
	if e != nil {
		return e
	}
	after, e := current.Stat(".")
	if e != nil {
		return e
	}
	if !os.SameFile(before, after) {
		return conflict("canonical server directory changed during content transaction")
	}
	return nil
}
