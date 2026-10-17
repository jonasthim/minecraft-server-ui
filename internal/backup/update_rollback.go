package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// UpdateRollbackRequest is a separately confirmed administrator operation. An
// ordinary update request cannot produce the backup-owned persisted consent.
type UpdateRollbackRequest struct {
	ParentTransactionID    string                  `json:"parent_transaction_id"`
	TransactionID          string                  `json:"transaction_id"`
	BackupID               string                  `json:"backup_id"`
	BackupSHA256           string                  `json:"backup_sha256"`
	TargetGeneration       string                  `json:"target_generation"`
	Target                 domain.VersionSelection `json:"target"`
	Confirmation           string                  `json:"confirmation"`
	AcknowledgeWorldRewind bool                    `json:"acknowledge_world_rewind"`
}
type RollbackReceipt struct {
	ParentTransactionID string                  `json:"parent_transaction_id"`
	TransactionID       string                  `json:"transaction_id"`
	InstanceID          string                  `json:"instance_id"`
	JobID               string                  `json:"job_id"`
	ActorID             int64                   `json:"actor_id"`
	BackupID            string                  `json:"backup_id"`
	BackupSHA256        string                  `json:"backup_sha256"`
	SafetyID            string                  `json:"safety_id"`
	TargetGeneration    string                  `json:"target_generation"`
	Target              domain.VersionSelection `json:"target"`
	TargetRuntime       string                  `json:"target_runtime"`
	ConsentedAt         time.Time               `json:"consented_at"`
}
type rollbackConsent struct {
	Version     int                     `json:"version"`
	Receipt     RollbackReceipt         `json:"receipt"`
	From        domain.VersionSelection `json:"from"`
	Revision    string                  `json:"revision"`
	LevelName   string                  `json:"level_name"`
	WorldRewind bool                    `json:"world_rewind"`
	MovesSHA256 string                  `json:"moves_sha256"`
}
type PreparedRollback struct {
	owner   *PreparedRollback
	capture *StoppedCapture
	consent rollbackConsent
	journal restoreJournal
}

func (p *PreparedRollback) Receipt() (RollbackReceipt, error) {
	if p == nil || p.owner != p {
		return RollbackReceipt{}, domain.E(domain.CodeForbidden, "rollback receipt forged or copied")
	}
	return p.consent.Receipt, nil
}
func validSHA256(value string) bool {
	_, err := hex.DecodeString(value)
	return len(value) == 64 && value == strings.ToLower(value) && err == nil
}

// PrepareUpdateRollback runs before the retained coordinator scope. It makes a
// new full current safety backup, validates the selected historical archive, and
// stages it using the same archive/digest engine as ordinary stopped restore.
func (s *Service) PrepareUpdateRollback(ctx context.Context, capture *StoppedCapture, session *instance.StoppedSession, request UpdateRollbackRequest) (*PreparedRollback, error) {
	if capture == nil || capture.owner != capture || !capture.active.Load() || capture.session != session {
		return nil, domain.E(domain.CodeForbidden, "original stopped capture required for rollback")
	}
	if !validKey(request.ParentTransactionID) || !validKey(request.TransactionID) || !validKey(request.BackupID) || !validKey(request.TargetGeneration) || !validSHA256(request.BackupSHA256) || request.Confirmation != capture.id || !request.AcknowledgeWorldRewind {
		return nil, domain.E(domain.CodeValidationFailed, "separate exact instance and world rewind confirmation required")
	}
	current, err := capture.execution.CheckCurrent(ctx)
	if err != nil {
		return nil, err
	}
	if current.Kind != "instance_rollback" || current.InstanceID != capture.id || current.JobID != capture.job || current.RequesterID != capture.actor.ID {
		return nil, domain.E(domain.CodeForbidden, "original explicit instance rollback job required")
	}
	actor, err := s.actor(ctx, capture.actor.ID)
	if err != nil || actor.Role != domain.RoleAdmin {
		return nil, domain.E(domain.CodeForbidden, "administrator rollback authority required")
	}
	detail, err := session.Detail(ctx)
	if err != nil {
		return nil, err
	}
	dirs, err := session.Paths(ctx)
	if err != nil {
		return nil, err
	}
	// Existing ordinary restore ownership must be clear. An update-owned blocker
	// is checked by the coordinator's exact consent admission, never weakened here.
	if err = s.updateRollbackPreconditions(ctx, dirs, capture.id, request.ParentTransactionID); err != nil {
		return nil, err
	}
	download, err := s.Download(ctx, capture.id, request.BackupID, actor)
	if err != nil {
		return nil, err
	}
	defer download.Close()
	record, err := s.record(ctx, capture.id, request.BackupID)
	if err != nil {
		return nil, err
	}
	if record.Kind != domain.BackupFull || download.SHA256 != request.BackupSHA256 {
		return nil, domain.E(domain.CodeConflict, "rollback requires exact historical full backup hash")
	}
	manifest, err := validateArchive(ctx, download.File, record.SizeBytes, defaultLimits)
	if err != nil {
		return nil, err
	}
	if manifest.InstanceID != capture.id || manifest.Version != request.Target.MinecraftVersion || manifest.Flavor != request.Target.Flavor || manifest.Loader != request.Target.LoaderVersion || manifest.LevelName != detail.Config.LevelName {
		return nil, domain.E(domain.CodeConflict, "rollback archive does not match exact historical selection")
	}
	var raw string
	if err = s.db.QueryRowContext(ctx, "SELECT installation_json FROM lifecycle_generations WHERE instance_id=? AND generation=?", capture.id, request.TargetGeneration).Scan(&raw); err != nil {
		return nil, domain.E(domain.CodeConflict, "rollback executable generation unavailable")
	}
	var history struct {
		Generation string             `json:"generation"`
		Plan       domain.InstallPlan `json:"plan"`
		Spec       domain.LaunchSpec  `json:"spec"`
	}
	if json.Unmarshal([]byte(raw), &history) != nil || history.Generation != request.TargetGeneration || history.Plan.Selection != request.Target || history.Spec.JavaRuntimeID != manifest.Runtime {
		return nil, domain.E(domain.CodeConflict, "rollback archive and retained generation differ")
	}
	safety, err := capture.Capture(ctx, domain.BackupFull, domain.BackupSafety)
	if err != nil {
		return nil, domain.Wrap(domain.CodeConflict, "new safety backup failed; world data unchanged", err)
	}
	if safety.State != domain.BackupReady {
		return nil, domain.E(domain.CodeConflict, "new rollback safety backup not validated")
	}
	journal, err := s.stageRestoreJournal(ctx, dirs, download.File, record.SizeBytes, manifest, request.TransactionID, request.BackupID, safety.ID, capture.job, capture.id)
	if err != nil {
		return nil, err
	}
	receipt := RollbackReceipt{ParentTransactionID: request.ParentTransactionID, TransactionID: request.TransactionID, InstanceID: capture.id, JobID: capture.job, ActorID: actor.ID, BackupID: request.BackupID, BackupSHA256: request.BackupSHA256, SafetyID: safety.ID, TargetGeneration: request.TargetGeneration, Target: request.Target, TargetRuntime: manifest.Runtime, ConsentedAt: time.Now().UTC()}
	consent := rollbackConsent{Version: 1, Receipt: receipt, From: domain.VersionSelection{Flavor: detail.Instance.Flavor, MinecraftVersion: detail.Instance.MinecraftVersion, LoaderVersion: detail.Instance.LoaderVersion}, Revision: detail.Instance.UpdatedAt.Format(time.RFC3339Nano), LevelName: detail.Config.LevelName, WorldRewind: true, MovesSHA256: rollbackMovesDigest(journal)}
	p := &PreparedRollback{capture: capture, consent: consent, journal: journal}
	p.owner = p
	return p, nil
}

// WithRollbackAdmission opens a separate durable intent transaction before the
// one final pointer commit scope. The original capture/session remain active;
// no world files or manager selection pointers may change in fn. The callback
// must perform synchronous supplied-tx calls only and the tx closes on return.
func (s *Service) WithRollbackAdmission(ctx context.Context, prepared *PreparedRollback, fn func(context.Context, *sql.Tx) error) (result error) {
	if prepared == nil || prepared.owner != prepared || prepared.capture == nil || !prepared.capture.active.Load() || fn == nil {
		return domain.E(domain.CodeForbidden, "prepared rollback authority expired")
	}
	c := prepared.capture
	if !c.mu.TryLock() {
		return domain.E(domain.CodeInstanceBusy, "rollback admission overlaps stopped capture")
	}
	defer c.mu.Unlock()
	if !c.active.Load() {
		return domain.E(domain.CodeForbidden, "stopped capture expired")
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	current, err := c.execution.CheckCurrent(operation)
	if err != nil {
		return err
	}
	if current.Kind != "instance_rollback" || current.JobID != c.job || current.InstanceID != c.id || current.RequesterID != c.actor.ID {
		return domain.E(domain.CodeForbidden, "rollback admission execution differs")
	}
	detail, err := c.session.Detail(operation)
	if err != nil {
		return err
	}
	if detail.Instance.UpdatedAt.Format(time.RFC3339Nano) != prepared.consent.Revision {
		return domain.E(domain.CodeConflict, "instance changed before rollback admission")
	}
	tx, err := s.db.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var synchronous int
	if err = tx.QueryRowContext(operation, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous < 2 {
		return domain.E(domain.CodeUnavailable, "rollback admission requires FULL SQLite durability")
	}
	if err = s.persistUpdateRollback(operation, tx, prepared); err != nil {
		return err
	}
	if err = fn(operation, tx); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Service) persistUpdateRollback(ctx context.Context, tx *sql.Tx, prepared *PreparedRollback) error {
	receipt := prepared.consent.Receipt
	if prepared.capture.id != receipt.InstanceID {
		return domain.E(domain.CodeForbidden, "rollback scope differs")
	}
	var role domain.Role
	var disabled bool
	if err := tx.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=?", receipt.ActorID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "rollback administrator authority revoked")
	}
	var kind string
	var actorID int64
	if err := tx.QueryRowContext(ctx, "SELECT kind,requested_by FROM jobs WHERE id=? AND instance_id=? AND state='running'", receipt.JobID, receipt.InstanceID).Scan(&kind, &actorID); err != nil || kind != "instance_rollback" || actorID != receipt.ActorID {
		return domain.E(domain.CodeForbidden, "rollback job authority changed")
	}
	for _, backupID := range []string{receipt.BackupID, receipt.SafetyID} {
		var metadata, state string
		if err := tx.QueryRowContext(ctx, "SELECT metadata_json,state FROM backups WHERE id=? AND instance_id=?", backupID, receipt.InstanceID).Scan(&metadata, &state); err != nil {
			return err
		}
		var record domain.Backup
		if json.Unmarshal([]byte(metadata), &record) != nil || state != "ready" || record.ID != backupID || record.InstanceID != receipt.InstanceID || record.Kind != domain.BackupFull {
			return domain.E(domain.CodeConflict, "rollback archive identity changed before admission")
		}
		if backupID == receipt.BackupID && (record.Checksum.Algorithm != "sha256" || record.Checksum.Value != receipt.BackupSHA256) {
			return domain.E(domain.CodeConflict, "rollback target archive hash changed")
		}
		if backupID == receipt.SafetyID && (!record.Protected || record.Trigger != domain.BackupSafety || record.ID == receipt.BackupID) {
			return domain.E(domain.CodeConflict, "new current safety archive changed")
		}
	}
	var historyRaw string
	if err := tx.QueryRowContext(ctx, "SELECT installation_json FROM lifecycle_generations WHERE instance_id=? AND generation=?", receipt.InstanceID, receipt.TargetGeneration).Scan(&historyRaw); err != nil {
		return err
	}
	var history struct {
		Generation string             `json:"generation"`
		Plan       domain.InstallPlan `json:"plan"`
		Spec       domain.LaunchSpec  `json:"spec"`
	}
	if json.Unmarshal([]byte(historyRaw), &history) != nil || history.Generation != receipt.TargetGeneration || history.Plan.Selection != receipt.Target || history.Spec.JavaRuntimeID != receipt.TargetRuntime {
		return domain.E(domain.CodeConflict, "rollback retained history changed before admission")
	}
	consent, _ := json.Marshal(prepared.consent)
	journal, _ := json.Marshal(prepared.journal)
	_, err := tx.ExecContext(ctx, `INSERT INTO backup_update_rollbacks(transaction_id,instance_id,job_id,actor_id,backup_id,safety_id,consent_json,journal_json,state,updated_at) VALUES(?,?,?,?,?,?,?,?,'prepared',?)`, receipt.TransactionID, receipt.InstanceID, receipt.JobID, receipt.ActorID, receipt.BackupID, receipt.SafetyID, string(consent), string(journal), stamp())
	return err
}

type RollbackParticipant struct {
	state   string
	owner   *RollbackParticipant
	active  atomic.Bool
	mu      sync.Mutex
	service *Service
	scope   *instance.TransactionScope
	consent rollbackConsent
	journal restoreJournal
}

func (p *RollbackParticipant) Receipt() (RollbackReceipt, error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return RollbackReceipt{}, domain.E(domain.CodeForbidden, "rollback participant expired")
	}
	return p.consent.Receipt, nil
}
func (p *RollbackParticipant) enter() (func(), error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return nil, domain.E(domain.CodeForbidden, "rollback participant forged or expired")
	}
	if !p.mu.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "rollback participant overlaps")
	}
	if !p.active.Load() {
		p.mu.Unlock()
		return nil, domain.E(domain.CodeForbidden, "rollback participant expired")
	}
	return p.mu.Unlock, nil
}

// WithUpdateRollbackRecovery is reachable only for exact persisted explicit
// world rewind consent plus matching coordinator ownership and launch blocker.
// It never scans ordinary backup_restores and cannot turn an update into a restore.
func (s *Service) WithUpdateRollbackRecovery(ctx context.Context, scope *instance.TransactionScope, transactionID string, fn func(*RollbackParticipant) error) error {
	if !validKey(transactionID) || fn == nil {
		return domain.E(domain.CodeValidationFailed, "exact rollback transaction required")
	}
	p := &RollbackParticipant{service: s, scope: scope}
	p.owner = p
	err := scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		var consentRaw, journalRaw, instanceID, jobID, backupID, safetyID string
		var actorID int64
		if err := s.db.QueryRowContext(operation, `SELECT consent_json,journal_json,instance_id,job_id,actor_id,backup_id,safety_id,state FROM backup_update_rollbacks WHERE transaction_id=? AND state!='complete'`, transactionID).Scan(&consentRaw, &journalRaw, &instanceID, &jobID, &actorID, &backupID, &safetyID, &p.state); err != nil {
			return err
		}
		if json.Unmarshal([]byte(consentRaw), &p.consent) != nil || json.Unmarshal([]byte(journalRaw), &p.journal) != nil {
			return errCorrupt
		}
		c := p.consent
		r := c.Receipt
		if c.Version != 1 || !c.WorldRewind || !validSHA256(c.MovesSHA256) || c.MovesSHA256 != rollbackMovesDigest(p.journal) || r.TransactionID != transactionID || r.InstanceID != snapshot.Detail.Instance.ID || r.InstanceID != instanceID || r.JobID != jobID || r.ActorID != actorID || r.BackupID != backupID || r.SafetyID != safetyID || !validSHA256(r.BackupSHA256) || r.ConsentedAt.IsZero() || !validKey(r.TargetGeneration) || !validKey(r.ParentTransactionID) || p.journal.OperationID != transactionID || p.journal.JobID != jobID || p.journal.InstanceID != instanceID || p.journal.BackupID != backupID || p.journal.SafetyID != safetyID || c.LevelName != snapshot.Detail.Config.LevelName {
			return domain.E(domain.CodeConflict, "persisted explicit rollback consent differs")
		}
		canonical, _ := json.Marshal(c)
		if string(canonical) != consentRaw {
			return errCorrupt
		}
		manifest := p.journal.Manifest
		if manifest.InstanceID != instanceID || manifest.Kind != domain.BackupFull || manifest.Version != r.Target.MinecraftVersion || manifest.Flavor != r.Target.Flavor || manifest.Loader != r.Target.LoaderVersion || manifest.Runtime != r.TargetRuntime || manifest.LevelName != c.LevelName {
			return domain.E(domain.CodeConflict, "rollback manifest differs from exact consent")
		}
		return p.checkOwnership(operation, snapshot)
	})
	if err != nil {
		return err
	}
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *RollbackParticipant) checkOwnership(ctx context.Context, snapshot instance.ScopeSnapshot) error {
	r := p.consent.Receipt
	var kind, jobID, backupID, parentID string
	var actorID int64
	if err := p.service.db.QueryRowContext(ctx, "SELECT kind,job_id,actor_id,backup_id,parent_transaction_id FROM update_transactions WHERE id=? AND instance_id=? AND state NOT IN ('complete','rolled_back','superseded')", r.TransactionID, r.InstanceID).Scan(&kind, &jobID, &actorID, &backupID, &parentID); err != nil {
		return err
	}
	if kind != "instance_rollback" || parentID != r.ParentTransactionID || jobID != r.JobID || actorID != r.ActorID || backupID != r.BackupID {
		return domain.E(domain.CodeConflict, "coordinator has no exact persisted world rewind consent")
	}
	var reciprocal bool
	if err := p.service.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions parent JOIN update_transactions child ON child.id=parent.superseded_by WHERE parent.id=? AND parent.instance_id=? AND parent.kind='update' AND parent.state='superseded' AND parent.parent_transaction_id IS NULL AND child.id=? AND child.instance_id=parent.instance_id AND child.parent_transaction_id=parent.id AND child.kind='instance_rollback' AND child.superseded_by IS NULL AND child.state!='superseded')`, r.ParentTransactionID, r.InstanceID, r.TransactionID).Scan(&reciprocal); err != nil {
		return err
	}
	if !reciprocal {
		return domain.E(domain.CodeConflict, "rollback successor has no exact one-hop parent")
	}
	target, err := p.service.record(ctx, r.InstanceID, r.BackupID)
	if err != nil {
		return err
	}
	safety, err := p.service.record(ctx, r.InstanceID, r.SafetyID)
	if err != nil {
		return err
	}
	if target.Kind != domain.BackupFull || target.Checksum.Algorithm != "sha256" || target.Checksum.Value != r.BackupSHA256 || safety.Kind != domain.BackupFull || safety.Trigger != domain.BackupSafety || !safety.Protected || safety.ID == target.ID {
		return domain.E(domain.CodeConflict, "rollback archive or new safety identity changed")
	}
	// Metadata cannot prove that an archived file still exists with the consented
	// bytes. Rehash both archives before any resumed move, using the existing
	// checked archive reader. The target manifest must equal the move journal.
	actor, err := p.service.actor(ctx, r.ActorID)
	if err != nil || actor.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "rollback administrator authority revoked")
	}
	for _, record := range []domain.Backup{target, safety} {
		download, err := p.service.Download(ctx, r.InstanceID, record.ID, actor)
		if err != nil {
			return err
		}
		manifest, readErr := validateArchive(ctx, download.File, record.SizeBytes, defaultLimits)
		closeErr := download.Close()
		if err = errors.Join(readErr, closeErr); err != nil {
			return err
		}
		if record.ID == target.ID && !reflect.DeepEqual(manifest, p.journal.Manifest) {
			return domain.E(domain.CodeConflict, "historical archive manifest changed")
		}
		if record.ID == safety.ID && (manifest.Version != p.consent.From.MinecraftVersion || manifest.Flavor != p.consent.From.Flavor || manifest.Loader != p.consent.From.LoaderVersion || manifest.LevelName != p.consent.LevelName) {
			return domain.E(domain.CodeConflict, "new current safety archive selection changed")
		}
	}
	dataDir := filepath.Dir(filepath.Dir(snapshot.Paths.Root))
	marker, present, err := launcher.ReadUpdateBlocker(ctx, dataDir, r.InstanceID)
	if err != nil {
		return err
	}
	if !present || marker.TransactionID != r.TransactionID || marker.JobID != r.JobID || marker.Kind != "instance_rollback" {
		return domain.E(domain.CodeConflict, "rollback coordinator marker differs")
	}
	risk, presentRisk, err := launcher.ReadWorldRiskBlocker(ctx, dataDir, r.InstanceID)
	if err != nil {
		return err
	}
	var parentJob string
	if err = p.service.db.QueryRowContext(ctx, "SELECT job_id FROM update_transactions WHERE id=? AND instance_id=?", r.ParentTransactionID, r.InstanceID).Scan(&parentJob); err != nil {
		return err
	}
	if !presentRisk || risk.TransactionID != r.ParentTransactionID || risk.Kind != "update" || risk.InstanceID != r.InstanceID || risk.JobID != parentJob {
		return domain.E(domain.CodeConflict, "world risk marker differs from explicit rollback parent")
	}
	return nil
}

// Progress phases are mutable; the originally consented move identities and
// manifest are immutable and checked before reaching the shared rename engine.
func rollbackMovesDigest(journal restoreJournal) string {
	copy := journal
	copy.DataReplaced = false
	copy.Moves = append([]restoreMove{}, journal.Moves...)
	for i := range copy.Moves {
		copy.Moves[i].Phase = "pending"
	}
	raw, _ := json.Marshal(copy)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (p *RollbackParticipant) checkpoint(ctx context.Context, journal *restoreJournal, state string) error {
	if state != "replacing" {
		return errCorrupt
	}
	raw, _ := json.Marshal(journal)
	result, err := p.service.db.ExecContext(ctx, "UPDATE backup_update_rollbacks SET state='replacing',journal_json=?,updated_at=? WHERE transaction_id=? AND consent_json=? AND state IN ('prepared','replacing','data_replaced')", string(raw), stamp(), p.consent.Receipt.TransactionID, p.consentJSON())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.E(domain.CodeConflict, "rollback consent or journal ownership changed")
	}
	return nil
}
func (p *RollbackParticipant) consentJSON() string {
	raw, _ := json.Marshal(p.consent)
	return string(raw)
}
func (p *RollbackParticipant) Replace(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	return p.scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		if err := p.checkOwnership(operation, snapshot); err != nil {
			return err
		}
		if err := p.service.replaceRestoreMoves(operation, snapshot.Paths, &p.journal, p.checkpoint); err != nil {
			return err
		}
		if p.state == "committed" {
			return nil
		}
		p.journal.DataReplaced = true
		raw, _ := json.Marshal(p.journal)
		result, err := p.service.db.ExecContext(operation, "UPDATE backup_update_rollbacks SET state='data_replaced',journal_json=?,updated_at=? WHERE transaction_id=? AND consent_json=?", string(raw), stamp(), p.consent.Receipt.TransactionID, p.consentJSON())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return errCorrupt
		}
		p.state = "data_replaced"
		return nil
	})
}
func (p *RollbackParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.journal.DataReplaced {
		return domain.E(domain.CodeConflict, "rollback data replacement incomplete")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		result, err := tx.ExecContext(operation, "UPDATE backup_update_rollbacks SET state='committed',updated_at=? WHERE transaction_id=? AND consent_json=? AND state='data_replaced'", stamp(), p.consent.Receipt.TransactionID, p.consentJSON())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return domain.E(domain.CodeConflict, "rollback receipt changed before pointer commit")
		}
		return nil
	})
}
func (p *RollbackParticipant) Finish(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	return p.scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		if err := p.checkOwnership(operation, snapshot); err != nil {
			return err
		}
		// Re-read restored file digests. Materialization may change only the reviewed
		// typed server properties and EULA; saved data cannot change under stopped admission.
		if err := p.service.replaceRestoreMoves(operation, snapshot.Paths, &p.journal, p.checkpoint); err != nil {
			return err
		}
		result, err := p.service.db.ExecContext(operation, "UPDATE backup_update_rollbacks SET state='complete',updated_at=? WHERE transaction_id=? AND consent_json=? AND state='committed'", stamp(), p.consent.Receipt.TransactionID, p.consentJSON())
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return domain.E(domain.CodeConflict, "rollback pointer commit is not durable")
		}
		return nil
	})
}

// updateRollbackPreconditions differs only in allowing the exact selected
// update coordinator's protected activation blocker. Ordinary restore remains
// strict; ordinary content/config/pack/backup ownership is never bypassed.
func (s *Service) updateRollbackPreconditions(ctx context.Context, dirs instance.Directories, id, parentID string) error {
	state, err := openSource(dirs.State)
	if err != nil {
		return err
	}
	defer state.Close()
	for _, name := range []string{"config.pending.json", "content-transaction.json", "modpack-transaction.json", "backup-restore.json"} {
		_, err = state.root.Lstat(name)
		if err == nil {
			return domain.E(domain.CodeConflict, "ordinary file recovery must complete before instance rollback")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	var busy bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_activations WHERE instance_id=?) OR EXISTS(SELECT 1 FROM content_journals WHERE instance_id=?) OR EXISTS(SELECT 1 FROM modpack_journals WHERE instance_id=?) OR EXISTS(SELECT 1 FROM backup_restores WHERE instance_id=? AND state!='complete')`, id, id, id, id).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return domain.E(domain.CodeConflict, "ordinary recovery owns this instance")
	}
	var kind, status, jobID string
	if err = s.db.QueryRowContext(ctx, "SELECT kind,state,job_id FROM update_transactions WHERE id=? AND instance_id=?", parentID, id).Scan(&kind, &status, &jobID); err != nil {
		return err
	}
	if kind != "update" || (status != "complete" && status != "boot_attempted" && status != "blocked") {
		return domain.E(domain.CodeConflict, "selected update cannot hand off instance rollback")
	}
	marker, present, err := launcher.ReadUpdateBlocker(ctx, filepath.Dir(filepath.Dir(dirs.Root)), id)
	if err != nil {
		return err
	}
	if present && (marker.TransactionID != parentID || marker.JobID != jobID || marker.Kind != "update") {
		return domain.E(domain.CodeConflict, "foreign update blocker prevents rollback")
	}
	if status != "complete" && !present {
		return domain.E(domain.CodeConflict, "unresolved update has no exact blocker")
	}
	return s.CheckReady(ctx, id)
}
