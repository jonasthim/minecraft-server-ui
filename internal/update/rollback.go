package update

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

type rollbackCoordinator struct{ service *Service }

func NewRollbackCoordinator(s *Service) (RollbackCoordinator, error) {
	if s == nil || s.config.DB == nil || s.config.Instances == nil || s.config.Jobs == nil || s.config.Backups == nil || s.config.Lifecycle == nil || s.config.Content == nil || s.config.Packs == nil {
		return nil, domain.E(domain.CodeUnavailable, "complete explicit rollback capabilities required")
	}
	return &rollbackCoordinator{service: s}, nil
}

// Only persisted normal update history can select a rewind target. This private
// reader intentionally ignores normal executable file operations: worlds are
// restored exclusively by backup-owned, separately consented move journals.
type rollbackSource struct {
	Version       int                   `json:"version"`
	Plan          Plan                  `json:"plan"`
	Before        lifecycle.UpdateState `json:"before"`
	After         lifecycle.UpdateState `json:"after"`
	ContentBefore content.Manifest      `json:"content_before"`
	ContentAfter  []content.Installed   `json:"content_after"`
	PackBefore    *modpack.Baseline     `json:"pack_before"`
	PackAfter     *modpack.Baseline     `json:"pack_after"`
	BootAttempted bool                  `json:"boot_attempted"`
}

func decodeRollbackSource(raw json.RawMessage) (rollbackSource, error) {
	var source rollbackSource
	if len(raw) > 32<<20 || json.Unmarshal(raw, &source) != nil || source.Version != 1 || !source.BootAttempted || !validHex(source.Before.Generation, 32) || !validHex(source.After.Generation, 32) || source.Before.Generation == source.After.Generation || source.Before.Generation != source.Plan.Snapshot.Generation || source.Before.Revision != source.Plan.Snapshot.InstanceRevision || source.Before.Plan.Selection != source.Plan.From || source.After.Plan.Selection != source.Plan.Target || source.Before.Spec.JavaRuntimeID != source.Before.Runtime.ID || source.After.Spec.JavaRuntimeID != source.After.Runtime.ID || source.Before.Runtime.ID == "" || source.After.Runtime.ID == "" {
		return source, conflict("selected update has no exact boot-attempted retained history")
	}
	if err := exact(source.Before.Plan.Selection); err != nil {
		return source, err
	}
	if err := exact(source.After.Plan.Selection); err != nil {
		return source, err
	}
	target := source.Before.Plan.Selection
	if source.ContentBefore.Tuple != (content.Tuple{Minecraft: target.MinecraftVersion, Loader: string(target.Flavor), LoaderVersion: target.LoaderVersion}) || !validHex(source.ContentBefore.Revision, 64) || source.ContentBefore.Revision != source.Plan.Snapshot.ContentRevision {
		return source, conflict("historical content identity differs from selected update")
	}
	beforeRaw, _ := json.Marshal(struct {
		Tuple content.Tuple
		Items []content.Installed
	}{source.ContentBefore.Tuple, source.ContentBefore.Items})
	packRevision := "none"
	if source.PackBefore != nil {
		packRevision = source.PackBefore.Revision
	}
	if rollbackDigest(beforeRaw) != source.ContentBefore.Revision || packRevision != source.Plan.Snapshot.PackRevision || len(source.ContentBefore.Items) > 2048 || len(source.ContentAfter) > 2048 {
		return source, conflict("historical manifest or pack snapshot changed")
	}
	return source, nil
}

type rollbackJournal struct {
	Version        int                    `json:"version"`
	Preview        RollbackPreview        `json:"preview"`
	ParentSHA256   string                 `json:"parent_sha256"`
	Receipt        backup.RollbackReceipt `json:"receipt"`
	CurrentContent content.Manifest       `json:"current_content"`
	CurrentPack    *modpack.Baseline      `json:"current_pack"`
}

func rollbackDigest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (c *rollbackCoordinator) parent(ctx context.Context, id, parentID string) (rollbackSource, string, string, string, error) {
	var raw, job, state, backupID, kind string
	err := c.service.config.DB.QueryRowContext(ctx, "SELECT journal_json,job_id,state,backup_id,kind FROM update_transactions WHERE id=? AND instance_id=?", parentID, id).Scan(&raw, &job, &state, &backupID, &kind)
	if err != nil {
		return rollbackSource{}, "", "", "", err
	}
	if kind != "update" {
		return rollbackSource{}, "", "", "", conflict("rollback source must be an original update")
	}
	source, err := decodeRollbackSource(json.RawMessage(raw))
	if err != nil {
		return source, "", "", "", err
	}
	if source.Plan.InstanceID != id {
		return source, "", "", "", conflict("rollback source belongs to another instance")
	}
	return source, raw, job, backupID, nil
}
func (c *rollbackCoordinator) current(ctx context.Context, id string) (Snapshot, content.Manifest, *modpack.Baseline, error) {
	s := c.service
	state, err := s.config.Lifecycle.UpdateSnapshot(ctx, id)
	if err != nil {
		return Snapshot{}, content.Manifest{}, nil, err
	}
	manifest, err := s.config.Content.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, manifest, nil, err
	}
	pack, err := s.config.Packs.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, manifest, nil, err
	}
	revision := "none"
	if pack.Baseline != nil {
		revision = pack.Baseline.Revision
	}
	return Snapshot{InstanceRevision: state.Revision, Generation: state.Generation, ContentRevision: manifest.Revision, PackRevision: revision}, manifest, pack.Baseline, nil
}
func (c *rollbackCoordinator) Preview(ctx context.Context, id, parentID string, actor domain.User) (RollbackPreview, error) {
	s := c.service
	fresh, err := s.actor(ctx, actor.ID, true)
	if err != nil {
		return RollbackPreview{}, err
	}
	if domain.ValidateInstanceID(id) != nil || !validHex(parentID, 32) {
		return RollbackPreview{}, invalid("exact instance update history required")
	}
	if err = s.checkOrdinaryReady(ctx, id); err != nil {
		return RollbackPreview{}, err
	}
	source, _, parentJob, backupID, err := c.parent(ctx, id, parentID)
	if err != nil {
		return RollbackPreview{}, err
	}
	var state string
	var superseded sql.NullString
	if err = s.config.DB.QueryRowContext(ctx, "SELECT state,superseded_by FROM update_transactions WHERE id=?", parentID).Scan(&state, &superseded); err != nil {
		return RollbackPreview{}, err
	}
	if superseded.Valid || (state != "boot_attempted" && state != "blocked") {
		return RollbackPreview{}, conflict("selected failed update cannot admit a world rewind")
	}
	current, manifest, baseline, err := c.current(ctx, id)
	if err != nil {
		return RollbackPreview{}, err
	}
	if current.Generation != source.After.Generation || !reflect.DeepEqual(manifest.Items, source.ContentAfter) || !reflect.DeepEqual(baseline, source.PackAfter) {
		return RollbackPreview{}, conflict("current pointers differ from selected failed update")
	}
	if err = c.checkParentMarker(ctx, id, parentID, parentJob); err != nil {
		return RollbackPreview{}, err
	}
	download, err := s.config.Backups.Download(ctx, id, backupID, fresh)
	if err != nil {
		return RollbackPreview{}, err
	}
	defer download.Close()
	previewID, err := opaqueID()
	if err != nil {
		return RollbackPreview{}, err
	}
	preview := RollbackPreview{ID: previewID, InstanceID: id, ParentTransactionID: parentID, ActorID: fresh.ID, Snapshot: current, BackupID: backupID, BackupSHA256: download.SHA256, TargetGeneration: source.Before.Generation, Target: source.Before.Plan.Selection, ExpiresAt: time.Now().UTC().Add(30 * time.Minute)}
	raw, err := json.Marshal(preview)
	if err != nil {
		return RollbackPreview{}, err
	}
	if _, err = s.config.DB.ExecContext(ctx, "INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at) VALUES(?,?,?,?,?,?)", preview.ID, id, fresh.ID, string(raw), stamp(), preview.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
		return RollbackPreview{}, err
	}
	if err = s.audit(ctx, id, fresh, "rollback_preview", "prepared"); err != nil {
		return RollbackPreview{}, err
	}
	return preview, nil
}
func (c *rollbackCoordinator) checkParentMarker(ctx context.Context, id, parent, job string) error {
	s := c.service
	data := pathsDataDir(s.config.Paths, id)
	expected := launcher.UpdateBlocker{Version: 1, InstanceID: id, TransactionID: parent, JobID: job, Kind: "update"}
	actual, present, err := launcher.ReadUpdateBlocker(ctx, data, id)
	if err != nil {
		return err
	}
	if !present || actual != expected {
		return conflict("exact selected failed update marker required")
	}
	risk, present, err := launcher.ReadWorldRiskBlocker(ctx, data, id)
	if err != nil {
		return err
	}
	if !present || risk != expected {
		return conflict("exact boot-attempted world risk marker required")
	}
	return nil
}
func (c *rollbackCoordinator) loadPreview(ctx context.Context, id, previewID string, actorID int64) (RollbackPreview, error) {
	var p RollbackPreview
	if domain.ValidateInstanceID(id) != nil || !validHex(previewID, 32) {
		return p, invalid("exact rollback preview required")
	}
	var raw, expires string
	if err := c.service.config.DB.QueryRowContext(ctx, "SELECT private_json,expires_at FROM update_previews WHERE id=? AND instance_id=? AND actor_id=?", previewID, id, actorID).Scan(&raw, &expires); err != nil {
		return p, err
	}
	if len(raw) > 65536 || json.Unmarshal([]byte(raw), &p) != nil || p.ID != previewID || p.InstanceID != id || p.ActorID != actorID || p.ExpiresAt.Format(time.RFC3339Nano) != expires || !time.Now().Before(p.ExpiresAt) || !validHex(p.ParentTransactionID, 32) || !validHex(p.BackupID, 32) || !validHex(p.BackupSHA256, 64) || !validHex(p.TargetGeneration, 32) {
		return p, conflict("rollback preview expired or malformed")
	}
	return p, nil
}
func (c *rollbackCoordinator) Enqueue(ctx context.Context, id, previewID, confirmation string, ack bool, actor domain.User) (jobs.Job, error) {
	s := c.service
	fresh, err := s.actor(ctx, actor.ID, true)
	if err != nil {
		return jobs.Job{}, err
	}
	if confirmation != id || !ack {
		return jobs.Job{}, invalid("separate exact instance confirmation and world rewind acknowledgement required")
	}
	if _, err = c.loadPreview(ctx, id, previewID, fresh.ID); err != nil {
		return jobs.Job{}, err
	}
	raw, err := json.Marshal(rollbackJobInput{PreviewID: previewID, Confirmation: confirmation, AcknowledgeWorldRewind: ack})
	if err != nil {
		return jobs.Job{}, err
	}
	if err = s.audit(ctx, id, fresh, "rollback_request", "requested"); err != nil {
		return jobs.Job{}, err
	}
	return s.config.Jobs.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "instance_rollback", RequestedBy: fresh.ID, Payload: raw, Cancellable: false})
}
func (c *rollbackCoordinator) Run(ctx context.Context, e *jobs.Execution) error {
	s := c.service
	current, actor, err := s.execution(ctx, e)
	if err != nil {
		return err
	}
	if current.Kind != "instance_rollback" {
		return domain.E(domain.CodeForbidden, "original explicit rollback execution required")
	}
	// Public e.Job/e.Payload are diagnostic copies, never request authority.
	var raw string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND requested_by=? AND kind='instance_rollback' AND state='running'", current.JobID, current.InstanceID, actor.ID).Scan(&raw); err != nil {
		return err
	}
	var input rollbackJobInput
	if json.Unmarshal([]byte(raw), &input) != nil || input.Confirmation != current.InstanceID || !input.AcknowledgeWorldRewind {
		return invalid("separate exact world rewind confirmation required")
	}
	preview, err := c.loadPreview(ctx, current.InstanceID, input.PreviewID, actor.ID)
	if err != nil {
		return err
	}
	if err = s.CheckJobReady(ctx, e, current.InstanceID); err != nil {
		return err
	}
	if err = s.checkOrdinaryReady(ctx, current.InstanceID); err != nil {
		return err
	}
	transaction, err := opaqueID()
	if err != nil {
		return err
	}
	return s.config.Backups.WithStoppedJob(ctx, e, func(capture *backup.StoppedCapture, session *instance.StoppedSession) error {
		snapshot, manifest, baseline, err := c.current(ctx, current.InstanceID)
		if err != nil {
			return err
		}
		if snapshot != preview.Snapshot {
			return conflict("rollback preview snapshot changed")
		}
		source, parentRaw, parentJob, backupID, err := c.parent(ctx, current.InstanceID, preview.ParentTransactionID)
		if err != nil {
			return err
		}
		if backupID != preview.BackupID || source.Before.Generation != preview.TargetGeneration || source.Before.Plan.Selection != preview.Target || source.After.Generation != snapshot.Generation || !reflect.DeepEqual(source.ContentAfter, manifest.Items) || !reflect.DeepEqual(source.PackAfter, baseline) {
			return conflict("rollback target or current history changed")
		}
		if err = c.checkParentMarker(ctx, current.InstanceID, preview.ParentTransactionID, parentJob); err != nil {
			return err
		}
		prepared, err := s.config.Backups.PrepareUpdateRollback(ctx, capture, session, backup.UpdateRollbackRequest{ParentTransactionID: preview.ParentTransactionID, TransactionID: transaction, BackupID: preview.BackupID, BackupSHA256: preview.BackupSHA256, TargetGeneration: preview.TargetGeneration, Target: preview.Target, Confirmation: input.Confirmation, AcknowledgeWorldRewind: input.AcknowledgeWorldRewind})
		if err != nil {
			return err
		}
		receipt, err := prepared.Receipt()
		if err != nil {
			return err
		}
		journal := rollbackJournal{Version: 1, Preview: preview, ParentSHA256: rollbackDigest([]byte(parentRaw)), Receipt: receipt, CurrentContent: manifest, CurrentPack: baseline}
		journalRaw, err := json.Marshal(journal)
		if err != nil {
			return err
		}
		if err = s.config.Backups.WithRollbackAdmission(ctx, prepared, func(operation context.Context, tx *sql.Tx) error {
			if err := checkRollbackAdmission(operation, tx, journal, source); err != nil {
				return err
			}
			// Backup inserts consent/safety/journal in this SAME admission tx.
			result, err := tx.ExecContext(operation, "UPDATE update_transactions SET state='superseded',superseded_by=?,updated_at=? WHERE id=? AND instance_id=? AND kind='update' AND state IN ('boot_attempted','blocked') AND superseded_by IS NULL AND journal_json=? AND backup_id=?", transaction, stamp(), preview.ParentTransactionID, current.InstanceID, parentRaw, preview.BackupID)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return conflict("rollback parent changed before admission")
			}
			if _, err = tx.ExecContext(operation, "INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,parent_transaction_id,journal_json,updated_at) VALUES(?,?,?,?,?,'instance_rollback','prepared',?,?,?,?)", transaction, current.InstanceID, preview.ID, current.JobID, actor.ID, preview.BackupID, preview.ParentTransactionID, string(journalRaw), stamp()); err != nil {
				return err
			}
			return ValidateSupersession(operation, tx)
		}); err != nil {
			return err
		}
		if err = c.transfer(ctx, journal, parentJob); err != nil {
			return err
		}
		return session.WithCoordinatorScope(ctx, func(operation context.Context, scope *instance.TransactionScope) error {
			return c.restore(operation, scope, journal, source, actor)
		})
	})
}

// Admission rechecks the complete observed pointer/history identities through
// the supplied original transaction after potentially lengthy safety capture.
func checkRollbackAdmission(ctx context.Context, tx *sql.Tx, j rollbackJournal, source rollbackSource) error {
	p := j.Preview
	if !time.Now().Before(p.ExpiresAt) {
		return conflict("rollback preview expired before admission")
	}
	expectedPreview, _ := json.Marshal(p)
	var raw, expires, revision, generation string
	if err := tx.QueryRowContext(ctx, "SELECT private_json,expires_at FROM update_previews WHERE id=? AND instance_id=? AND actor_id=?", p.ID, p.InstanceID, p.ActorID).Scan(&raw, &expires); err != nil {
		return err
	}
	if raw != string(expectedPreview) || expires != p.ExpiresAt.Format(time.RFC3339Nano) {
		return conflict("rollback preview changed before admission")
	}
	if err := tx.QueryRowContext(ctx, "SELECT i.updated_at,l.generation,l.installation_json FROM instances i JOIN lifecycle_installations l ON l.instance_id=i.id WHERE i.id=?", p.InstanceID).Scan(&revision, &generation, &raw); err != nil {
		return err
	}
	var current struct {
		Generation string             `json:"generation"`
		Plan       domain.InstallPlan `json:"plan"`
		Spec       domain.LaunchSpec  `json:"spec"`
	}
	if revision != p.Snapshot.InstanceRevision || generation != p.Snapshot.Generation || json.Unmarshal([]byte(raw), &current) != nil || current.Generation != generation || !reflect.DeepEqual(current.Plan, source.After.Plan) || !reflect.DeepEqual(current.Spec, source.After.Spec) {
		return conflict("current installation changed before rollback admission")
	}
	if err := tx.QueryRowContext(ctx, "SELECT installation_json FROM lifecycle_generations WHERE instance_id=? AND generation=?", p.InstanceID, p.TargetGeneration).Scan(&raw); err != nil {
		return err
	}
	if json.Unmarshal([]byte(raw), &current) != nil || current.Generation != p.TargetGeneration || !reflect.DeepEqual(current.Plan, source.Before.Plan) || !reflect.DeepEqual(current.Spec, source.Before.Spec) {
		return conflict("retained target changed before rollback admission")
	}
	rows, err := tx.QueryContext(ctx, "SELECT manifest_json FROM content_items WHERE instance_id=? ORDER BY id", p.InstanceID)
	if err != nil {
		return err
	}
	items := []content.Installed{}
	for rows.Next() {
		var raw string
		var item content.Installed
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			break
		}
		items = append(items, item)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !reflect.DeepEqual(items, j.CurrentContent.Items) {
		return conflict("content pointer changed before rollback admission")
	}
	var baseline *modpack.Baseline
	err = tx.QueryRowContext(ctx, "SELECT baseline_json FROM modpack_baselines WHERE instance_id=?", p.InstanceID).Scan(&raw)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		baseline = &modpack.Baseline{}
		if err = json.Unmarshal([]byte(raw), baseline); err != nil {
			return err
		}
	}
	if !reflect.DeepEqual(baseline, j.CurrentPack) {
		return conflict("pack baseline changed before rollback admission")
	}
	return nil
}
func (c *rollbackCoordinator) transfer(ctx context.Context, j rollbackJournal, parentJob string) error {
	old := launcher.UpdateBlocker{Version: 1, InstanceID: j.Receipt.InstanceID, TransactionID: j.Receipt.ParentTransactionID, JobID: parentJob, Kind: "update"}
	next := launcher.UpdateBlocker{Version: 1, InstanceID: j.Receipt.InstanceID, TransactionID: j.Receipt.TransactionID, JobID: j.Receipt.JobID, Kind: "instance_rollback"}
	return launcher.TransferUpdateBlocker(ctx, pathsDataDir(c.service.config.Paths, j.Receipt.InstanceID), old, next)
}
