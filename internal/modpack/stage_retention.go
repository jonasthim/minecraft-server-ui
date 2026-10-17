package modpack

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

type stageRetentionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Caller holds s.mu, including across uploads, stage publication and removal.
// A terminal SQL row alone does not release uncertain recovery: independent
// markers and active participants conservatively retain the instance's stages.
func (s *Service) stagesHeld(ctx context.Context, id string, reader stageRetentionReader) (bool, error) {
	var held bool
	err := reader.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND state NOT IN ('complete','rolled_back','superseded')) OR
 EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE instance_id=? AND state!='complete') OR
 EXISTS(SELECT 1 FROM content_journals WHERE instance_id=?) OR
 EXISTS(SELECT 1 FROM modpack_journals WHERE instance_id=?)`, id, id, id, id).Scan(&held)
	if err != nil || held {
		return held, err
	}
	root, err := s.stateRoot(ctx, id)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	for _, name := range []string{"content-transaction.json", "modpack-transaction.json", "update-transaction.json", "update-world-risk.json"} {
		if _, err := root.Lstat(name); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, ctx.Err()
}

// A quota retry sweeps expired artifacts across instances: stale reservations in
// another instance count against the global bound too. Live preview/job receipts,
// recovery holds and all independent transaction/backup material are retained.
func (s *Service) expireStageQuota(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT DISTINCT instance_id FROM modpack_stages ORDER BY instance_id")
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.expire(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
