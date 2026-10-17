package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

func (s *Service) autostartSnapshot(ctx context.Context, id string) (*lifecycle.AutostartSuspension, error) {
	policy := &lifecycle.AutostartSuspension{}
	var applied sql.NullBool
	err := s.config.DB.QueryRowContext(ctx, "SELECT desired,applied,error FROM lifecycle_autostart WHERE instance_id=?", id).Scan(&policy.PriorDesired, &applied, &policy.PriorError)
	if err == sql.ErrNoRows {
		return policy, nil
	}
	if err != nil {
		return nil, err
	}
	policy.PriorPresent = true
	if applied.Valid {
		value := applied.Bool
		policy.PriorApplied = &value
	}
	return policy, nil
}
func (s *Service) checkNormalJobReady(ctx context.Context, e *jobs.Execution, current jobs.CurrentExecution, id string) error {
	if current.InstanceID != id || current.Kind != "instance_update" {
		return domain.E(domain.CodeForbidden, "original typed update continuation required")
	}
	if err := s.checkOrdinaryReady(ctx, id); err != nil {
		return err
	}
	var raw, transaction, preview, backup string
	var actor int64
	if err := s.config.DB.QueryRowContext(ctx, `SELECT id,preview_id,backup_id,actor_id,journal_json FROM update_transactions WHERE instance_id=? AND job_id=? AND kind='update' AND state='prepared' AND superseded_by IS NULL`, id, current.JobID).Scan(&transaction, &preview, &backup, &actor, &raw); err != nil {
		return err
	}
	if actor != current.RequesterID {
		return domain.E(domain.CodeForbidden, "prepared update requester changed")
	}
	j, err := decodeJournal(raw, id)
	if err != nil {
		return err
	}
	if j.BootAttempted || j.Autostart == nil || !j.Autostart.DisableConfirmed || j.Plan.ID != preview || len(j.Plan.Blockers) != 0 {
		return conflict("prepared continuation receipt is incomplete")
	}
	fresh, err := s.actor(ctx, actor, false)
	if err != nil {
		return err
	}
	admin, err := s.PreviewNeedsAdministrator(ctx, id, j.Request, actor)
	if err != nil {
		return err
	}
	if admin && fresh.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "administrator update authority revoked")
	}
	snapshot, err := s.currentSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if snapshot != j.Plan.Snapshot {
		return conflict("prepared update before pointers changed")
	}
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND requested_by=? AND kind='instance_update' AND state='running'", current.JobID, id, actor).Scan(&raw); err != nil {
		return err
	}
	var input jobInput
	if json.Unmarshal([]byte(raw), &input) != nil {
		return invalid("durable update request is malformed")
	}
	switch input.Action {
	case "automatic":
		if j.Request.Mode != Automatic || j.Plan.Mode != Automatic {
			return conflict("automatic update receipt differs")
		}
		if _, _, err = j.Request.Resolve(j.Plan.From); err != nil {
			return err
		}
	case "apply":
		if input.PreviewID != preview || !input.Confirmed && j.Plan.Mode == Manual {
			return conflict("manual update confirmation differs")
		}
		envelope, err := s.loadPreview(ctx, id, preview, actor)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(envelope.Request, j.Request) {
			return conflict("prepared request differs from confirmed preview")
		}
		j.Plan.ActorID = actor
		expected, expectedErr := Freeze(envelope.Plan)
		actual, actualErr := Freeze(j.Plan)
		if expectedErr != nil || actualErr != nil || expected.Digest() != actual.Digest() {
			return conflict("prepared plan differs from confirmed preview")
		}
	default:
		return invalid("prepared update action differs")
	}
	var metadata string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT metadata_json FROM backups WHERE id=? AND instance_id=? AND state='ready'", backup, id).Scan(&metadata); err != nil {
		return err
	}
	var safety domain.Backup
	if json.Unmarshal([]byte(metadata), &safety) != nil || safety.ID != backup || safety.InstanceID != id || safety.Kind != domain.BackupFull || !safety.Protected || safety.State != domain.BackupReady {
		return conflict("prepared update safety archive is not ready and protected")
	}
	marker, present, err := launcher.ReadUpdateBlocker(ctx, pathsDataDir(s.config.Paths, id), id)
	if err != nil {
		return err
	}
	expected := launcher.UpdateBlocker{Version: 1, InstanceID: id, TransactionID: transaction, JobID: current.JobID, Kind: "update"}
	if !present || marker != expected {
		return conflict("prepared update marker differs")
	}
	if _, present, err = launcher.ReadWorldRiskBlocker(ctx, pathsDataDir(s.config.Paths, id), id); err != nil {
		return err
	}
	if present {
		return conflict("world-risk marker blocks normal continuation")
	}
	_, err = e.CheckCurrent(ctx)
	return err
}
