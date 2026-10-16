package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"time"
)

type Retention struct {
	Count  int
	MaxAge time.Duration
}

var DefaultRetention = Retention{Count: 10, MaxAge: 30 * 24 * time.Hour}

func retentionCandidates(backups []domain.Backup, policy Retention, now time.Time) []string {
	out := []string{}
	rank := 0
	for _, b := range backups {
		if b.State != domain.BackupReady || b.Trigger != domain.BackupAutomatic || b.Protected {
			continue
		}
		rank++
		if rank > policy.Count || now.Sub(b.CreatedAt) > policy.MaxAge {
			out = append(out, b.ID)
		}
	}
	return out
}
func (s *Service) applyRetention(ctx context.Context, id string, policy Retention, now time.Time) error {
	if policy.Count < 1 || policy.MaxAge <= 0 {
		return errCorrupt
	}
	rows, err := s.db.QueryContext(ctx, `SELECT metadata_json FROM backups WHERE instance_id=? AND state='ready' ORDER BY created_at DESC,id DESC`, id)
	if err != nil {
		return err
	}
	backups := []domain.Backup{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		var b domain.Backup
		if json.Unmarshal([]byte(raw), &b) != nil {
			rows.Close()
			return errCorrupt
		}
		backups = append(backups, b)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, backupID := range retentionCandidates(backups, policy, now) {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		if s.refs[backupID] > 0 {
			s.mu.Unlock()
			continue
		}
		var referenced bool
		err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_restores WHERE (backup_id=? OR safety_id=?) AND state!='complete') OR EXISTS(SELECT 1 FROM jobs WHERE instance_id=? AND kind='backup_restore' AND state IN ('queued','running') AND json_extract(payload,'$.backup_id')=?)`, backupID, backupID, id, backupID).Scan(&referenced)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		if referenced {
			s.mu.Unlock()
			continue
		}
		_, err = s.db.ExecContext(ctx, `UPDATE backups SET state='deleting',recovery='Automatic retention deletion pending.' WHERE id=? AND state='ready'`, backupID)
		s.mu.Unlock()
		if err != nil {
			return err
		}
		if err = s.deleteRetained(ctx, id, backupID); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) deleteRetained(ctx context.Context, id, backupID string) error {
	if !validKey(backupID) {
		return errCorrupt
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	root, err := openSource(dirs.Backups)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.verify(); err != nil {
		return err
	}
	err = root.root.Remove(backupID + ".zip")
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = syncDir(root.root, "."); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM backups WHERE id=? AND state='deleting'`, backupID)
	return err
}
