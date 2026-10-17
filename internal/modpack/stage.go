package modpack

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io"
	"os"
	"time"
)

func (s *Service) stageRoot(ctx context.Context, id string) (*os.Root, error) {
	d, e := s.paths.For(id)
	if e != nil {
		return nil, e
	}
	root, e := safepath.OpenRoot(ctx, d.State)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	if e = root.Mkdir("modpack", 0700); e != nil && !os.IsExist(e) {
		return nil, e
	}
	if e = syncDir(root); e != nil {
		return nil, e
	}
	before, e := root.Lstat("modpack")
	if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, invalid("unsafe pack staging root")
	}
	child, e := root.OpenRoot("modpack")
	if e != nil {
		return nil, e
	}
	after, e := child.Stat(".")
	if e != nil || !os.SameFile(before, after) {
		child.Close()
		return nil, conflict("pack staging root changed")
	}
	return child, nil
}

// Caller holds service.mu. Reservations precede bytes and survive interruption.
func (s *Service) stage(ctx context.Context, id string, actor int64, r io.Reader, size int64) (stage, error) {
	return s.stageChecked(ctx, id, actor, r, size, nil)
}
func (s *Service) stageChecked(ctx context.Context, id string, actor int64, r io.Reader, size int64, check func(context.Context, *sql.Tx, stage) error) (out stage, result error) {
	if size < 0 || size > MaxFileBytes {
		return out, invalid("pack stage size exceeds bound")
	}
	var count, total, global int64
	if e := s.db.QueryRowContext(ctx, "SELECT count(*),coalesce(sum(size),0) FROM modpack_stages WHERE instance_id=?", id).Scan(&count, &total); e != nil {
		return out, e
	}
	if e := s.db.QueryRowContext(ctx, "SELECT coalesce(sum(size),0) FROM modpack_stages").Scan(&global); e != nil {
		return out, e
	}
	if count >= 20000 || total+size > 8<<30 || global+size > 16<<30 {
		return out, conflict("pack staging quota reached")
	}
	out = stage{ID: newID(), Size: size}
	if _, e := s.db.ExecContext(ctx, "INSERT INTO modpack_stages(id,instance_id,actor_id,sha256,size,created_at) VALUES(?,?,?,?,?,?)", out.ID, id, actor, "", size, time.Now().UTC().Format(time.RFC3339Nano)); e != nil {
		return out, e
	}
	root, e := s.stageRoot(ctx, id)
	if e != nil {
		return out, e
	}
	defer root.Close()
	f, e := root.OpenFile(out.ID+".blob", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return out, e
	}
	keep := false
	defer func() {
		f.Close()
		if !keep {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			remove := root.Remove(out.ID + ".blob")
			if remove == nil || os.IsNotExist(remove) {
				_, remove = s.db.ExecContext(cleanup, "DELETE FROM modpack_stages WHERE id=?", out.ID)
			}
			result = errors.Join(result, remove)
		}
	}()
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(contextReader{ctx, r}, size+1))
	if e != nil {
		return out, e
	}
	if n != size {
		return out, invalid("pack stage size mismatch")
	}
	if e = f.Sync(); e != nil {
		return out, e
	}
	if e = f.Close(); e != nil {
		return out, e
	}
	if e = syncDir(root); e != nil {
		return out, e
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	if check == nil {
		if _, e = s.db.ExecContext(ctx, "UPDATE modpack_stages SET sha256=? WHERE id=?", out.SHA256, out.ID); e != nil {
			return out, e
		}
	} else {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err = check(ctx, tx, out); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE modpack_stages SET sha256=? WHERE id=?", out.SHA256, out.ID); err != nil {
			return out, err
		}
		if err = tx.Commit(); err != nil {
			return out, err
		}
	}
	keep = true
	return out, nil
}
func (s *Service) openStage(ctx context.Context, id string, st stage) (*os.File, error) {
	if !validHex(st.ID, 32) || !validHex(st.SHA256, 64) || st.Size < 0 || st.Size > MaxFileBytes {
		return nil, invalid("invalid private pack stage reference")
	}
	var hash string
	var size int64
	if e := s.db.QueryRowContext(ctx, "SELECT sha256,size FROM modpack_stages WHERE instance_id=? AND id=?", id, st.ID).Scan(&hash, &size); e != nil {
		return nil, e
	}
	if hash != st.SHA256 || size != st.Size {
		return nil, conflict("pack stage metadata changed")
	}
	root, e := s.stageRoot(ctx, id)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	f, e := openChecked(root, st.ID+".blob", MaxFileBytes)
	if e != nil {
		return nil, e
	}
	h := sha256.New()
	n, e := io.Copy(h, contextReader{ctx, f})
	if e != nil || n != size || hex.EncodeToString(h.Sum(nil)) != hash {
		f.Close()
		return nil, conflict("pack staged bytes changed")
	}
	_, e = f.Seek(0, io.SeekStart)
	if e != nil {
		f.Close()
		return nil, e
	}
	return f, nil
}
