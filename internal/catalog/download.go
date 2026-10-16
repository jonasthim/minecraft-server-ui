package catalog

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validArtifactURL(p Provider, raw string) bool {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(u.Path, "\\\x00\r\n") {
		return false
	}
	allowed := map[Provider][]string{Modrinth: {"cdn.modrinth.com"}, CurseForge: {"edge.forgecdn.net", "mediafilez.forgecdn.net"}, Hangar: {"hangarcdn.papermc.io"}}
	if !contains(allowed[p], u.Host) {
		return false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return u.Path != "" && u.Path != "/"
}
func validFilename(name string) bool {
	if name == "" || len(name) > 255 || strings.HasSuffix(name, ".") || strings.TrimSpace(name) != name || strings.ContainsAny(name, `/\:<>"|?*`) {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if contains([]string{"CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$"}, base) {
		return false
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return false
	}
	return true
}
func usableHashes(h map[string]string) bool {
	found := false
	for alg, n := range map[string]int{"sha1": 40, "sha256": 64, "sha512": 128} {
		v, ok := h[alg]
		if !ok {
			continue
		}
		b, e := hex.DecodeString(v)
		if e != nil || len(v) != n || len(b)*2 != n {
			return false
		}
		found = true
	}
	return found
}
func normalizeFile(p Provider, f File) File {
	if f.Hashes == nil {
		f.Hashes = map[string]string{}
	}
	switch {
	case !validFilename(f.Filename):
		f.ManualReason = "Unsafe or missing publisher filename."
	case !validArtifactURL(p, f.URL):
		f.ManualReason = "Direct download unavailable or outside approved publisher CDN; use the official page."
	case f.Size <= 0 || f.Size > MaxArtifactBytes:
		f.ManualReason = "Missing or oversized published artifact size."
	case !usableHashes(f.Hashes):
		f.ManualReason = "Missing or invalid published SHA integrity; manual review required."
	}
	if f.ManualReason != "" {
		f.URL = ""
	}
	return f
}
func (s *Service) Download(ctx context.Context, selection Selection) (VerifiedArtifact, error) {
	v, e := s.Version(ctx, selection.Provider, selection.ProjectID, selection.VersionID)
	if e != nil {
		return VerifiedArtifact{}, e
	}
	for _, f := range v.Files {
		if f.ID == selection.FileID {
			a, e := s.downloadFile(ctx, selection, f, v.Compatibility)
			a.Provenance = v.Provenance
			return a, e
		}
	}
	return VerifiedArtifact{}, domain.NotFound("catalog file")
}
func (s *Service) downloadFile(ctx context.Context, selection Selection, f File, c Compatibility) (artifact VerifiedArtifact, resultErr error) {
	f = normalizeFile(selection.Provider, f)
	if f.ManualReason != "" {
		return VerifiedArtifact{}, domain.E(domain.CodeUnsupported, f.ManualReason)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		return VerifiedArtifact{}, domain.Wrap(domain.CodeUnavailable, "catalog download canceled", ctx.Err())
	}
	tmp, e := os.CreateTemp(s.dir, ".catalog-stage-")
	if e != nil {
		return VerifiedArtifact{}, fmt.Errorf("stage catalog download: %w", e)
	}
	tmpName := tmp.Name()
	defer func() {
		closeErr := tmp.Close()
		if errors.Is(closeErr, os.ErrClosed) {
			closeErr = nil
		}
		removeErr := os.Remove(tmpName)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		if cleanupErr := errors.Join(closeErr, removeErr); cleanupErr != nil {
			resultErr = domain.Wrap(domain.CodeUnavailable, "catalog staged artifact cleanup failed; review private artifact storage", errors.Join(resultErr, cleanupErr))
		}
	}()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if e != nil {
		return VerifiedArtifact{}, upstream("invalid artifact request")
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	resp, e := s.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return VerifiedArtifact{}, domain.Wrap(domain.CodeUpstreamError, "artifact download canceled or timed out", ctx.Err())
		}
		return VerifiedArtifact{}, upstream("artifact download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength > 0 && resp.ContentLength != f.Size {
		return VerifiedArtifact{}, upstream("artifact size/status does not match publisher metadata")
	}
	hashes := map[string]hash.Hash{"sha256": sha256.New()}
	for alg := range f.Hashes {
		switch alg {
		case "sha1":
			hashes[alg] = sha1.New()
		case "sha512":
			hashes[alg] = sha512.New()
		}
	}
	writers := []io.Writer{tmp}
	for _, h := range hashes {
		writers = append(writers, h)
	}
	n, e := io.Copy(io.MultiWriter(writers...), io.LimitReader(resp.Body, f.Size+1))
	if e != nil || n != f.Size {
		return VerifiedArtifact{}, upstream("artifact incomplete or exceeds published size")
	}
	if ctx.Err() != nil {
		return VerifiedArtifact{}, domain.Wrap(domain.CodeUpstreamError, "artifact download canceled", ctx.Err())
	}
	for alg, h := range hashes {
		if published, ok := f.Hashes[alg]; ok && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), published) {
			return VerifiedArtifact{}, upstream("artifact published hash mismatch")
		}
	}
	digest := hex.EncodeToString(hashes["sha256"].Sum(nil))
	if e = tmp.Sync(); e != nil {
		return VerifiedArtifact{}, fmt.Errorf("sync verified artifact: %w", e)
	}
	if e = tmp.Close(); e != nil {
		return VerifiedArtifact{}, fmt.Errorf("close verified artifact: %w", e)
	}
	destination := filepath.Join(s.dir, digest+".artifact")
	if ctx.Err() != nil {
		return VerifiedArtifact{}, domain.Wrap(domain.CodeUpstreamError, "artifact publication canceled", ctx.Err())
	}
	e = os.Link(tmpName, destination)
	if errors.Is(e, os.ErrExist) {
		if e = verifyExisting(ctx, destination, digest, f.Size); e != nil {
			return VerifiedArtifact{}, e
		}
	} else if e != nil {
		return VerifiedArtifact{}, fmt.Errorf("publish verified artifact: %w", e)
	}
	// Remove the writable staging alias before marking the published inode read-only.
	// Windows readonly attributes otherwise prevent deleting that alias.
	if e = os.Remove(tmpName); e != nil {
		return VerifiedArtifact{}, fmt.Errorf("remove published artifact staging alias: %w", e)
	}
	if e = os.Chmod(destination, 0400); e != nil {
		return VerifiedArtifact{}, fmt.Errorf("protect published artifact: %w", e)
	}
	if e = syncArtifactDirectory(s.dir); e != nil {
		return VerifiedArtifact{}, fmt.Errorf("sync artifact publication: %w", e)
	}
	return VerifiedArtifact{Selection: selection, Provenance: Provenance{selection.Provider, time.Now().UTC()}, File: f, Compatibility: c, SHA256: digest, Path: destination}, nil
}
func verifyExisting(ctx context.Context, path, digest string, size int64) error {
	fi, e := os.Lstat(path)
	if e != nil || !fi.Mode().IsRegular() || fi.Size() != size {
		return domain.E(domain.CodeConflict, "existing artifact is unsafe or corrupt")
	}
	f, e := os.Open(path)
	if e != nil {
		return fmt.Errorf("read existing artifact: %w", e)
	}
	defer f.Close()
	h := sha256.New()
	reader := &contextReader{ctx, f}
	if _, e = io.Copy(h, io.LimitReader(reader, size+1)); e != nil {
		return fmt.Errorf("verify existing artifact: %w", e)
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return domain.E(domain.CodeConflict, "existing artifact hash mismatch")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(p)
}
