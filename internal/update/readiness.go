package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func (s *Service) markerPending(ctx context.Context, id string) (bool, error) {
	paths, err := s.config.Paths.For(id)
	if err != nil {
		return true, err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	defer root.Close()
	for _, name := range []string{"update-transaction.json", "update-world-risk.json"} {
		_, err = root.Lstat(name)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return true, err
		}
	}
	return false, ctx.Err()
}
func (s *Service) CheckReady(ctx context.Context, id string) error {
	if domain.ValidateInstanceID(id) != nil {
		return invalid("invalid instance identity")
	}
	pending, err := s.markerPending(ctx, id)
	if err != nil {
		return err
	}
	if pending {
		return conflict("update or world compatibility recovery must finish before changing this instance")
	}
	var active bool
	err = s.config.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND state NOT IN ('complete','rolled_back','superseded')) OR EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE instance_id=? AND state!='complete')`, id, id).Scan(&active)
	if err != nil {
		return err
	}
	if active {
		return conflict("update transaction recovery is pending")
	}
	return nil
}

// HasPendingTransactions validates terminal ownership chains as well as active
// rows and scans all instance markers, so SQL loss cannot authorize activation.
func (s *Service) HasPendingTransactions(ctx context.Context) (bool, error) {
	tx, err := s.config.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if err = ValidateSupersession(ctx, tx); err != nil {
		return true, err
	}
	var pending bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE state NOT IN ('complete','rolled_back','superseded')) OR EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE state!='complete')`).Scan(&pending); err != nil {
		return true, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM instances ORDER BY id")
	if err != nil {
		return true, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return true, err
		}
		ids = append(ids, id)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return true, err
	}
	if err = tx.Commit(); err != nil {
		return true, err
	}
	for _, id := range ids {
		blocked, err := s.markerPending(ctx, id)
		if err != nil {
			return true, err
		}
		pending = pending || blocked
	}
	return pending, nil
}

// RollbackPreview binds separate consent to a known retained update history.
// The service persists this private envelope before admitting a rollback job.
type RollbackPreview struct {
	ID                  string                  `json:"id"`
	InstanceID          string                  `json:"instance_id"`
	ParentTransactionID string                  `json:"parent_transaction_id"`
	ActorID             int64                   `json:"actor_id"`
	Snapshot            Snapshot                `json:"snapshot"`
	BackupID            string                  `json:"backup_id"`
	BackupSHA256        string                  `json:"backup_sha256"`
	TargetGeneration    string                  `json:"target_generation"`
	Target              domain.VersionSelection `json:"target"`
	ExpiresAt           time.Time               `json:"expires_at"`
}
type rollbackJobInput struct {
	PreviewID              string `json:"preview_id"`
	Confirmation           string `json:"confirmation"`
	AcknowledgeWorldRewind bool   `json:"acknowledge_world_rewind"`
}

// CheckJobReady may bypass only the exact failed update's own blockers for an
// original separately confirmed administrator rollback. The integrator MUST
// retain ordinary backup/content/pack/config readiness checks before this gate.
func (s *Service) CheckJobReady(ctx context.Context, e *jobs.Execution, id string) error {
	if err := s.CheckReady(ctx, id); err == nil {
		return nil
	}
	current, err := e.CheckCurrent(ctx)
	if err != nil {
		return err
	}
	if current.Kind == "instance_update" {
		return s.checkNormalJobReady(ctx, e, current, id)
	}
	if current.InstanceID != id || current.Kind != "instance_rollback" {
		return domain.E(domain.CodeForbidden, "only original explicit instance rollback may continue update ownership")
	}
	var role domain.Role
	var disabled bool
	if err = s.config.DB.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=?", current.RequesterID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "fresh administrator rollback authority required")
	}
	var raw string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND kind='instance_rollback' AND state='running' AND requested_by=?", current.JobID, id, current.RequesterID).Scan(&raw); err != nil {
		return err
	}
	var input rollbackJobInput
	if json.Unmarshal([]byte(raw), &input) != nil || !validHex(input.PreviewID, 32) || input.Confirmation != id || !input.AcknowledgeWorldRewind {
		return domain.E(domain.CodeForbidden, "separate exact world rewind confirmation required")
	}
	if err = s.config.DB.QueryRowContext(ctx, "SELECT private_json FROM update_previews WHERE id=? AND instance_id=? AND actor_id=?", input.PreviewID, id, current.RequesterID).Scan(&raw); err != nil {
		return err
	}
	var preview RollbackPreview
	if json.Unmarshal([]byte(raw), &preview) != nil || preview.ID != input.PreviewID || preview.InstanceID != id || preview.ActorID != current.RequesterID || !validHex(preview.ParentTransactionID, 32) || !validHex(preview.TargetGeneration, 32) || !validHex(preview.BackupID, 32) || !validHex(preview.BackupSHA256, 64) || !time.Now().Before(preview.ExpiresAt) {
		return conflict("rollback preview is stale or malformed")
	}
	var parentJob, kind, state, generation, revision string
	if err = s.config.DB.QueryRowContext(ctx, `SELECT t.job_id,t.kind,t.state,g.generation,i.updated_at FROM update_transactions t JOIN instances i ON i.id=t.instance_id JOIN lifecycle_installations g ON g.instance_id=i.id WHERE t.id=? AND t.instance_id=? AND t.superseded_by IS NULL`, preview.ParentTransactionID, id).Scan(&parentJob, &kind, &state, &generation, &revision); err != nil {
		return err
	}
	if kind != "update" || (state != "boot_attempted" && state != "blocked") || generation != preview.Snapshot.Generation || revision != preview.Snapshot.InstanceRevision {
		return conflict("rollback history or current version changed")
	}
	var foreign bool
	if err = s.config.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND state NOT IN ('complete','rolled_back','superseded') AND id!=?) OR EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE instance_id=? AND state!='complete')`, id, preview.ParentTransactionID, id).Scan(&foreign); err != nil {
		return err
	}
	if foreign {
		return conflict("another update or rollback owns this instance")
	}
	marker, present, err := launcher.ReadUpdateBlocker(ctx, pathsDataDir(s.config.Paths, id), id)
	if err != nil {
		return err
	}
	if !present || marker.Kind != "update" || marker.TransactionID != preview.ParentTransactionID || marker.JobID != parentJob {
		return conflict("rollback does not own the exact failed update marker")
	}
	risk, riskPresent, err := launcher.ReadWorldRiskBlocker(ctx, pathsDataDir(s.config.Paths, id), id)
	if err != nil {
		return err
	}
	if riskPresent && risk != marker {
		return conflict("world risk ownership differs from selected failed update")
	}
	return nil
}
func pathsDataDir(paths instance.Paths, id string) string {
	dirs, err := paths.For(id)
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Dir(dirs.Root))
}
