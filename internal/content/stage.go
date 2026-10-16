package content

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Service) stageRoot(ctx context.Context, id string) (*os.Root, error) {
	d, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	state, e := safepath.OpenRoot(ctx, d.State)
	if e != nil {
		return nil, e
	}
	defer state.Close()
	if e = state.Mkdir("content", 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	if e = syncDir(state); e != nil {
		return nil, e
	}
	fi, e := state.Lstat("content")
	if e != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("unsafe private content staging directory")
	}
	root, e := state.OpenRoot("content")
	if e != nil {
		return nil, e
	}
	opened, e := root.Stat(".")
	if e != nil || !os.SameFile(fi, opened) {
		root.Close()
		return nil, conflict("staging identity changed")
	}
	return root, nil
}

// Caller holds s.mu: quota checks and ownership publication are serialized.
func (s *Service) stage(ctx context.Context, id string, actor int64, name string, r io.Reader, size int64) (_ Upload, result error) {
	var count, total, global int64
	if size < 0 || size > MaxArtifactBytes {
		return Upload{}, invalid("artifact exceeds 2 GiB limit")
	}
	if e := s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(size),0) FROM content_stages WHERE instance_id=?", id).Scan(&count, &total); e != nil {
		return Upload{}, e
	}
	if e := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(size),0) FROM content_stages").Scan(&global); e != nil {
		return Upload{}, e
	}
	if count >= 256 || total+size > 8<<30 || global+size > 16<<30 {
		return Upload{}, conflict("private artifact staging quota reached; discard unused previews/uploads")
	}
	root, e := s.stageRoot(ctx, id)
	if e != nil {
		return Upload{}, e
	}
	defer root.Close()
	u := Upload{ID: newID(), Filename: name}
	// Reserve quota durably before creating bytes; interrupted reservations remain
	// visible to quota accounting and expire only when unreferenced.
	_, e = s.db.ExecContext(ctx, "INSERT INTO content_stages(id,instance_id,actor_id,filename,sha256,size,created_at) VALUES(?,?,?,?,?,?,?)", u.ID, id, actor, name, "", size, time.Now().UTC().Format(time.RFC3339Nano))
	if e != nil {
		return u, e
	}
	keep, created := false, false
	var file *os.File
	defer func() {
		if file != nil {
			file.Close()
		}
		if !keep {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var removeErr error
			if created {
				checked, openErr := openChecked(root, u.ID+".blob", MaxArtifactBytes+1)
				removeErr = openErr
				if openErr == nil {
					removeErr = checked.Chmod(0600)
					checked.Close()
				}
				if removeErr == nil {
					removeErr = root.Remove(u.ID + ".blob")
				}
				if os.IsNotExist(removeErr) {
					removeErr = nil
				}
			}
			if removeErr == nil {
				_, removeErr = s.db.ExecContext(cleanup, "DELETE FROM content_stages WHERE id=?", u.ID)
			}
			result = errors.Join(result, removeErr)
		}
	}()
	file, e = root.OpenFile(u.ID+".blob", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return u, e
	}
	created = true
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(file, h), io.LimitReader(contextReader{ctx, r}, size+1))
	if e != nil {
		return u, fmt.Errorf("stage content: %w", e)
	}
	if n > size {
		return u, invalid("artifact exceeds declared staging bound")
	}
	if e = file.Chmod(0400); e != nil {
		return u, e
	}
	if e = file.Sync(); e != nil {
		return u, e
	}
	if e = file.Close(); e != nil {
		return u, e
	}
	if e = syncDir(root); e != nil {
		return u, e
	}
	u.Size = n
	u.SHA256 = hex.EncodeToString(h.Sum(nil))
	_, e = s.db.ExecContext(ctx, "UPDATE content_stages SET sha256=?,size=? WHERE id=?", u.SHA256, u.Size, u.ID)
	if e != nil {
		return u, e
	}
	keep = true
	return u, nil
}
func (s *Service) openStage(ctx context.Context, id, stage string) (Upload, *os.File, error) {
	u := Upload{ID: stage}
	if len(stage) != 32 || !portable(stage) {
		return u, nil, invalid("invalid stage identity")
	}
	e := s.db.QueryRowContext(ctx, "SELECT filename,sha256,size FROM content_stages WHERE id=? AND instance_id=?", stage, id).Scan(&u.Filename, &u.SHA256, &u.Size)
	if errors.Is(e, sql.ErrNoRows) {
		return u, nil, domain.NotFound("staged content")
	}
	if e != nil {
		return u, nil, e
	}
	root, e := s.stageRoot(ctx, id)
	if e != nil {
		return u, nil, e
	}
	defer root.Close()
	f, e := openChecked(root, stage+".blob", MaxArtifactBytes)
	if e != nil {
		return u, nil, e
	}
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(contextReader{ctx, f}, MaxArtifactBytes+1))
	if e != nil || n != u.Size || hex.EncodeToString(h.Sum(nil)) != u.SHA256 {
		f.Close()
		return u, nil, conflict("immutable content stage changed")
	}
	if _, e = f.Seek(0, 0); e != nil {
		f.Close()
		return u, nil, e
	}
	return u, f, nil
}
func (s *Service) StageUpload(ctx context.Context, id, name string, r io.Reader, actor domain.User) (Upload, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	default:
		return Upload{}, conflict("two content uploads are already active")
	}
	if _, e := s.actor(ctx, actor.ID, true); e != nil {
		return Upload{}, e
	}
	if !portable(name) || !strings.HasSuffix(strings.ToLower(name), ".jar") {
		return Upload{}, invalid("upload requires a portable JAR filename")
	}
	if _, e := s.instances.Get(ctx, id); e != nil {
		return Upload{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.expire(ctx, id); e != nil {
		return Upload{}, e
	}
	u, e := s.stage(ctx, id, actor.ID, name, r, MaxArtifactBytes)
	if e != nil {
		return u, e
	}
	_, f, e := s.openStage(ctx, id, u.ID)
	if e != nil {
		return u, e
	}
	defer f.Close()
	if _, e = inspectArchive(f, u.Size); e != nil {
		f.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return Upload{}, errors.Join(e, s.removeStage(cleanup, id, u.ID))
	}
	if e = s.audit(ctx, id, actor, "upload", u.ID, "staged"); e != nil {
		return u, e
	}
	return u, nil
}
func (s *Service) removeStage(ctx context.Context, id, stage string) error {
	r, e := s.stageRoot(ctx, id)
	if e != nil {
		return e
	}
	defer r.Close()
	name := stage + ".blob"
	if !portable(stage) || len(stage) != 32 {
		return invalid("invalid stage identity")
	}
	checked, openErr := openChecked(r, name, MaxArtifactBytes+1)
	if openErr != nil && !os.IsNotExist(openErr) {
		return openErr
	}
	if checked != nil {
		e = checked.Chmod(0600)
		checked.Close()
		if e != nil {
			return e
		}
	}
	if e = r.Remove(name); e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = syncDir(r); e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM content_stages WHERE id=? AND instance_id=?", stage, id)
	return e
}
func (s *Service) catalogStage(ctx context.Context, id string, actor int64, selection catalog.Selection) (catalog.Version, catalog.File, Upload, error) {
	var file catalog.File
	var upload Upload
	if s.catalog == nil {
		return catalog.Version{}, file, upload, domain.E(domain.CodeUnavailable, "catalog unavailable")
	}
	v, e := s.catalog.Version(ctx, selection.Provider, selection.ProjectID, selection.VersionID)
	if e != nil {
		return v, file, upload, domain.E(domain.CodeUpstreamError, "publisher version lookup failed")
	}
	for _, f := range v.Files {
		if f.ID != selection.FileID {
			continue
		}
		file = f
		if f.ManualReason != "" || f.URL == "" {
			return v, file, upload, nil
		}
		a, e := s.catalog.Download(ctx, selection)
		if e != nil {
			return v, file, upload, domain.E(domain.CodeUpstreamError, "verified publisher artifact unavailable")
		}
		if a.Selection != selection || a.File.ID != f.ID || a.SHA256 == "" {
			return v, file, upload, conflict("catalog artifact identity mismatch")
		}
		root, e := safepath.OpenRoot(ctx, filepath.Dir(a.Path))
		if e != nil {
			return v, file, upload, e
		}
		defer root.Close()
		reader, e := openChecked(root, filepath.Base(a.Path), MaxArtifactBytes)
		if e != nil {
			return v, file, upload, e
		}
		defer reader.Close()
		upload, e = s.stage(ctx, id, actor, f.Filename, reader, f.Size)
		if e == nil && (upload.SHA256 != a.SHA256 || upload.Size != f.Size) {
			return v, file, Upload{}, conflict("verified publisher artifact changed")
		}
		return v, file, upload, e
	}
	return v, file, upload, domain.NotFound("publisher file")
}
