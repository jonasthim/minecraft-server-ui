package backup

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io/fs"
)

// CheckReady protects ordinary mutations from outstanding saving repair and
// incomplete restore. Recovery admission intentionally bypasses this gate.
// Consumers must call it inside their existing admitted instance boundary.
func (s *Service) CheckReady(ctx context.Context, id string) error {
	if domain.ValidateInstanceID(id) != nil {
		return errCorrupt
	}
	var blocked bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_leases WHERE instance_id=? AND state IN ('armed','unresolved')) OR EXISTS(SELECT 1 FROM backup_restores WHERE instance_id=? AND state!='complete')`, id, id).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return domain.E(domain.CodeUnavailable, "saving or restore recovery must complete before changing server data")
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	state, err := openSource(dirs.State)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer state.Close()
	_, err = state.root.Lstat("backup-restore.json")
	if err == nil {
		return domain.E(domain.CodeUnavailable, "restore marker requires reconciliation before changing server data")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return state.verify()
}
