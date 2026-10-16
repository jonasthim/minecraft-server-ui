package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"encoding/hex"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"strings"
	"time"
)

// SupersessionReader allows readiness to validate under its own read-only
// snapshot transaction without opening a connection or reentering the DB.
type SupersessionReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ValidateSupersession checks every ownership handoff, including terminal ones.
// A terminal row count alone cannot establish that a world rollback completed.
// Pending but valid successors pass here and remain pending in readiness counts.
func ValidateSupersession(ctx context.Context, reader SupersessionReader) error {
	rows, err := reader.QueryContext(ctx, `SELECT p.id,p.instance_id,p.kind,p.state,COALESCE(p.superseded_by,''),COALESCE(s.id,''),COALESCE(s.instance_id,''),COALESCE(s.kind,''),COALESCE(s.state,''),COALESCE(s.parent_transaction_id,''),COALESCE(s.superseded_by,''),COALESCE(s.job_id,''),COALESCE(s.actor_id,0),COALESCE(s.backup_id,''),COALESCE(b.instance_id,''),COALESCE(b.job_id,''),COALESCE(b.actor_id,0),COALESCE(b.backup_id,''),COALESCE(b.safety_id,''),COALESCE(b.consent_json,''),COALESCE(b.state,''),COALESCE(target.metadata_json,''),COALESCE(target.state,''),COALESCE(safety.metadata_json,''),COALESCE(safety.state,'') FROM update_transactions p LEFT JOIN update_transactions s ON s.id=p.superseded_by LEFT JOIN backup_update_rollbacks b ON b.transaction_id=s.id LEFT JOIN backups target ON target.id=b.backup_id LEFT JOIN backups safety ON safety.id=b.safety_id WHERE p.state='superseded' OR p.superseded_by IS NOT NULL LIMIT 10001`)
	if err != nil {
		return fmt.Errorf("read update ownership handoffs: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 10000 {
			return supersessionError("update ownership history exceeds bounded drain validation")
		}
		var parentID, instanceID, parentKind, parentState, successorID, id, childInstance, kind, state, parentRef, next, jobID, backupID, receiptInstance, receiptJob, receiptBackup, safetyID, consentRaw, receiptState, targetRaw, targetState, safetyRaw, safetyState string
		var actor, receiptActor int64
		if err = rows.Scan(&parentID, &instanceID, &parentKind, &parentState, &successorID, &id, &childInstance, &kind, &state, &parentRef, &next, &jobID, &actor, &backupID, &receiptInstance, &receiptJob, &receiptActor, &receiptBackup, &safetyID, &consentRaw, &receiptState, &targetRaw, &targetState, &safetyRaw, &safetyState); err != nil {
			return err
		}
		if parentKind != "update" || parentState != "superseded" || id == "" || successorID != id || instanceID != childInstance || kind != "instance_rollback" || parentRef != parentID || next != "" || state == "superseded" || id == parentID {
			return supersessionError("update ownership handoff is missing, cyclic or mismatched")
		}
		var consent struct {
			Version     int                 `json:"version"`
			WorldRewind bool                `json:"world_rewind"`
			Receipt     supersessionReceipt `json:"receipt"`
		}
		if len(consentRaw) > 65536 || json.Unmarshal([]byte(consentRaw), &consent) != nil {
			return supersessionError("rollback consent is malformed")
		}
		r := consent.Receipt
		if consent.Version != 1 || !consent.WorldRewind || r.ParentTransactionID != parentID || r.TransactionID != id || r.InstanceID != instanceID || receiptInstance != instanceID || r.JobID != jobID || receiptJob != jobID || actor <= 0 || r.ActorID != actor || receiptActor != actor || r.BackupID != backupID || receiptBackup != backupID || safetyID == "" || r.SafetyID != safetyID || r.SafetyID == r.BackupID || !validSupersessionHex(r.BackupSHA256, 64) || !validSupersessionHex(r.TargetGeneration, 32) || r.ConsentedAt.IsZero() {
			return supersessionError("rollback consent does not bind the exact ownership handoff")
		}
		if state == "complete" && receiptState != "complete" {
			return supersessionError("completed rollback has unfinished backup receipt")
		}
		var target, safety domain.Backup
		if json.Unmarshal([]byte(targetRaw), &target) != nil || json.Unmarshal([]byte(safetyRaw), &safety) != nil || targetState != "ready" || safetyState != "ready" || target.ID != backupID || safety.ID != safetyID || target.InstanceID != instanceID || safety.InstanceID != instanceID || target.Kind != domain.BackupFull || safety.Kind != domain.BackupFull || target.Checksum.Algorithm != "sha256" || target.Checksum.Value != r.BackupSHA256 || safety.Trigger != domain.BackupSafety || !safety.Protected {
			return supersessionError("rollback archive or new safety backup identity is unavailable")
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	var malformed bool
	err = reader.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions child LEFT JOIN update_transactions parent ON parent.id=child.parent_transaction_id WHERE child.parent_transaction_id IS NOT NULL AND (parent.id IS NULL OR parent.instance_id!=child.instance_id OR parent.state!='superseded' OR parent.superseded_by!=child.id OR child.kind!='instance_rollback' OR parent.kind!='update' OR child.superseded_by IS NOT NULL OR child.state='superseded'))`).Scan(&malformed)
	if err != nil {
		return err
	}
	if malformed {
		return supersessionError("rollback successor has no exact reciprocal parent")
	}
	return nil
}

// supersessionReceipt is the read-only subset of the frozen backup consent
// JSON contract needed for drain. It imports no unfinished update/backup service.
type supersessionReceipt struct {
	ParentTransactionID string    `json:"parent_transaction_id"`
	TransactionID       string    `json:"transaction_id"`
	InstanceID          string    `json:"instance_id"`
	JobID               string    `json:"job_id"`
	ActorID             int64     `json:"actor_id"`
	BackupID            string    `json:"backup_id"`
	BackupSHA256        string    `json:"backup_sha256"`
	SafetyID            string    `json:"safety_id"`
	TargetGeneration    string    `json:"target_generation"`
	ConsentedAt         time.Time `json:"consented_at"`
}

func supersessionError(message string) error { return domain.E(domain.CodeConflict, message) }
func validSupersessionHex(value string, size int) bool {
	_, err := hex.DecodeString(value)
	return len(value) == size && value == strings.ToLower(value) && err == nil
}
