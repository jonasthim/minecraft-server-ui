package modpack

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"os"
	"strings"
	"time"
)

func verifyMember(ctx context.Context, r io.ReaderAt, size int64, file PackFile) error {
	if size != file.Size {
		return invalid("manual member size mismatch")
	}
	hashes, e := publishedHashes(file.Hashes)
	if e != nil {
		return e
	}
	writers := []io.Writer{}
	for _, h := range hashes {
		writers = append(writers, h)
	}
	n, e := io.Copy(io.MultiWriter(writers...), contextReader{ctx, io.NewSectionReader(r, 0, size)})
	if e != nil {
		return e
	}
	if n != size {
		return invalid("manual member incomplete")
	}
	for name, h := range hashes {
		if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), file.Hashes[name]) {
			return invalid("manual member published digest mismatch")
		}
	}
	return nil
}

// ResumeMember accepts raw bytes only for an exact unresolved member in an owned
// private preview. Its official hashes/size are never supplied by the API.
func (s *Service) ResumeMember(ctx context.Context, id, preview, member string, r io.Reader, actor domain.User) (out Preview, result error) {
	currentActor, e := s.actor(ctx, actor.ID, true)
	if e != nil {
		return Preview{}, e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		outcome := "verified"
		if result != nil {
			outcome = "failed_or_conflicted"
		}
		result = errors.Join(result, s.audit(cleanup, id, currentActor, "manual_resume", outcome))
	}()
	s.mu.Lock()
	p, e := s.loadPreview(ctx, id, preview, actor.ID)
	if e != nil {
		s.mu.Unlock()
		return Preview{}, e
	}
	if e = s.CheckReady(ctx, id); e != nil {
		s.mu.Unlock()
		return Preview{}, e
	}
	if job, e := s.liveJob(ctx, id, preview); e != nil || job != "" {
		s.mu.Unlock()
		return Preview{}, conflict("pack preview owned by live job")
	}
	file, ok := p.Members[member]
	pending := false
	for _, m := range p.Preview.Manual {
		if m.Path == member {
			pending = true
		}
	}
	if !ok || !pending || file.ArchiveEntry != "" {
		s.mu.Unlock()
		return Preview{}, invalid("manual member not unresolved in this preview")
	}
	st, e := s.stage(ctx, id, actor.ID, r, file.Size)
	if e == nil {
		var f interface {
			io.ReaderAt
			Close() error
		}
		f, e = s.openStage(ctx, id, st)
		if e == nil {
			e = verifyMember(ctx, f, st.Size, file)
			f.Close()
		}
	}
	if e != nil {
		s.mu.Unlock()
		return Preview{}, e
	}
	request := p.Request
	request.PreviousPreviewID = p.Preview.ID
	if request.Manual == nil {
		request.Manual = map[string]string{}
	}
	request.Manual[member] = st.ID
	s.mu.Unlock()
	next, e := s.Preview(ctx, id, request, actor)
	if e != nil {
		return next, e
	}
	if next.Source.SHA256 != p.Preview.Source.SHA256 {
		return next, conflict("pack changed during manual resume; review new preview")
	}
	return next, nil
}
func (s *Service) Discard(ctx context.Context, id, preview string, actor domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.actor(ctx, actor.ID, false); e != nil {
		return e
	}
	if _, e := s.loadPreview(ctx, id, preview, actor.ID); e != nil {
		return e
	}
	if e := s.CheckReady(ctx, id); e != nil {
		return e
	}
	job, e := s.liveJob(ctx, id, preview)
	if e != nil {
		return e
	}
	if job != "" {
		return conflict("pack preview owned by live job")
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM modpack_previews WHERE instance_id=? AND id=? AND actor_id=?", id, preview, actor.ID)
	if e != nil {
		return e
	}
	return s.cleanupStages(ctx, id, false)
}
func (s *Service) cleanupStages(ctx context.Context, id string, expired bool) error {
	query := "SELECT id FROM modpack_stages WHERE instance_id=? AND NOT EXISTS(SELECT 1 FROM modpack_previews WHERE instr(private_json,modpack_stages.id)>0) AND NOT EXISTS(SELECT 1 FROM modpack_journals WHERE instr(private_json,modpack_stages.id)>0) AND NOT EXISTS(SELECT 1 FROM modpack_archives WHERE id=modpack_stages.id)"
	args := []any{id}
	if expired {
		query += " AND created_at<?"
		args = append(args, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano))
	}
	rows, e := s.db.QueryContext(ctx, query, args...)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var stageID string
		if e = rows.Scan(&stageID); e != nil {
			break
		}
		ids = append(ids, stageID)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	root, e := s.stageRoot(ctx, id)
	if e != nil {
		return e
	}
	defer root.Close()
	for _, stageID := range ids {
		if !validHex(stageID, 32) {
			return conflict("invalid stage record")
		}
		if e = root.Remove(stageID + ".blob"); e != nil && !os.IsNotExist(e) {
			return e
		}
		if e = syncDir(root); e != nil {
			return e
		}
		if _, e = s.db.ExecContext(ctx, "DELETE FROM modpack_stages WHERE instance_id=? AND id=?", id, stageID); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) expire(ctx context.Context, id string) error {
	if _, e := s.db.ExecContext(ctx, "DELETE FROM modpack_archives WHERE instance_id=? AND created_at<? AND NOT EXISTS(SELECT 1 FROM modpack_previews WHERE instr(private_json,modpack_archives.id)>0) AND NOT EXISTS(SELECT 1 FROM modpack_journals WHERE instr(private_json,modpack_archives.id)>0)", id, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)); e != nil {
		return e
	}
	_, e := s.db.ExecContext(ctx, "DELETE FROM modpack_previews WHERE instance_id=? AND (state IN ('applied','superseded') OR created_at<?) AND NOT EXISTS(SELECT 1 FROM jobs WHERE jobs.instance_id=modpack_previews.instance_id AND kind='modpack_apply' AND state IN ('queued','running') AND json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')=modpack_previews.id) AND NOT EXISTS(SELECT 1 FROM modpack_journals WHERE preview_id=modpack_previews.id)", id, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano))
	if e != nil {
		return e
	}
	return s.cleanupStages(ctx, id, true)
}
