package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

// Reconcile can finish ONLY the persisted explicit consent. It cannot mint a
// preview, new consent, a safety archive, or any native start on startup.
func (c *rollbackCoordinator) Reconcile(ctx context.Context, id, transaction string, provided json.RawMessage) error {
	s := c.service
	if domain.ValidateInstanceID(id) != nil || !validHex(transaction, 32) {
		return invalid("exact rollback recovery owner required")
	}
	var raw, state string
	if err := s.config.DB.QueryRowContext(ctx, "SELECT journal_json,state FROM update_transactions WHERE id=? AND instance_id=? AND kind='instance_rollback'", transaction, id).Scan(&raw, &state); err != nil {
		return err
	}
	if string(provided) != raw {
		return conflict("rollback recovery journal is not the persisted owner")
	}
	j, source, parentJob, err := c.validateRecovery(ctx, id, transaction, raw)
	if err != nil {
		return err
	}
	actor, err := s.actor(ctx, j.Receipt.ActorID, true)
	if err != nil {
		return err
	}
	return s.config.Instances.WithStoppedRecovery(ctx, id, func(session *instance.StoppedSession) error {
		if err := s.checkOrdinaryReady(ctx, id); err != nil {
			return err
		}
		// Recheck after paused stopped admission, including reciprocal one-hop links.
		if _, _, _, err = c.validateRecovery(ctx, id, transaction, raw); err != nil {
			return err
		}
		marker, present, err := launcher.ReadUpdateBlocker(ctx, pathsDataDir(s.config.Paths, id), id)
		if err != nil {
			return err
		}
		next := launcher.UpdateBlocker{Version: 1, InstanceID: id, TransactionID: transaction, JobID: j.Receipt.JobID, Kind: "instance_rollback"}
		if state != "complete" {
			if !present {
				return conflict("unfinished explicit rollback blocker missing")
			}
			if err = c.transfer(ctx, j, parentJob); err != nil {
				return err
			}
		} else if present && marker != next {
			return conflict("completed rollback has foreign blocker")
		}
		if state == "complete" && !present {
			risk, riskPresent, err := launcher.ReadWorldRiskBlocker(ctx, pathsDataDir(s.config.Paths, id), id)
			if err != nil {
				return err
			}
			if !riskPresent {
				return nil
			}
			expected := launcher.UpdateBlocker{Version: 1, InstanceID: id, TransactionID: j.Receipt.ParentTransactionID, JobID: parentJob, Kind: "update"}
			if risk != expected {
				return conflict("completed rollback has foreign world risk marker")
			}
			if err = launcher.WriteUpdateBlocker(ctx, pathsDataDir(s.config.Paths, id), next); err != nil {
				return err
			}
		}
		return session.WithCoordinatorScope(ctx, func(operation context.Context, scope *instance.TransactionScope) error {
			return c.restore(operation, scope, j, source, actor)
		})
	})
}

func (c *rollbackCoordinator) validateRecovery(ctx context.Context, id, transaction, raw string) (rollbackJournal, rollbackSource, string, error) {
	var j rollbackJournal
	if len(raw) > 32<<20 || json.Unmarshal([]byte(raw), &j) != nil || j.Version != 1 {
		return j, rollbackSource{}, "", conflict("rollback journal malformed")
	}
	r := j.Receipt
	p := j.Preview
	if r.TransactionID != transaction || r.InstanceID != id || r.ActorID <= 0 || r.ParentTransactionID != p.ParentTransactionID || r.ActorID != p.ActorID || p.InstanceID != id || r.BackupID != p.BackupID || r.BackupSHA256 != p.BackupSHA256 || r.TargetGeneration != p.TargetGeneration || r.Target != p.Target || !validHex(r.TransactionID, 32) || !validHex(r.JobID, 32) || !validHex(r.ParentTransactionID, 32) || !validHex(r.SafetyID, 32) || !validHex(r.BackupID, 32) || !validHex(r.BackupSHA256, 64) || !validHex(r.TargetGeneration, 32) || r.ConsentedAt.IsZero() || r.SafetyID == r.BackupID || !validHex(j.ParentSHA256, 64) {
		return j, rollbackSource{}, "", conflict("rollback journal consent identity differs")
	}
	s := c.service
	// Validate every chain before any marker handoff or file restoration, including
	// terminal rows. A competing/orphan/cyclic history never reaches Replace.
	if err := ValidateSupersession(ctx, s.config.DB); err != nil {
		return j, rollbackSource{}, "", err
	}
	var competing bool
	if err := s.config.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND id!=? AND state NOT IN ('complete','rolled_back','superseded')) OR EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE instance_id=? AND transaction_id!=? AND state!='complete')`, id, transaction, id, transaction).Scan(&competing); err != nil {
		return j, rollbackSource{}, "", err
	}
	if competing {
		return j, rollbackSource{}, "", conflict("competing rollback ownership")
	}
	source, parentRaw, parentJob, targetID, err := c.parent(ctx, id, r.ParentTransactionID)
	if err != nil {
		return j, source, "", err
	}
	if rollbackDigest([]byte(parentRaw)) != j.ParentSHA256 || targetID != r.BackupID || source.Before.Generation != r.TargetGeneration || source.Before.Plan.Selection != r.Target || source.Before.Spec.JavaRuntimeID != r.TargetRuntime || source.After.Generation != p.Snapshot.Generation || j.CurrentContent.Revision != p.Snapshot.ContentRevision || !reflect.DeepEqual(j.CurrentContent.Items, source.ContentAfter) || !reflect.DeepEqual(j.CurrentPack, source.PackAfter) {
		return j, source, "", conflict("retained rollback history changed")
	}
	var consentRaw, jobID, backupID, parentID, previewID, persistedJournal string
	var actorID int64
	if err = s.config.DB.QueryRowContext(ctx, "SELECT job_id,actor_id,backup_id,parent_transaction_id,preview_id,journal_json FROM update_transactions WHERE id=? AND instance_id=? AND kind='instance_rollback'", transaction, id).Scan(&jobID, &actorID, &backupID, &parentID, &previewID, &persistedJournal); err != nil {
		return j, source, "", err
	}
	if jobID != r.JobID || actorID != r.ActorID || backupID != r.BackupID || parentID != r.ParentTransactionID || previewID != p.ID || persistedJournal != raw {
		return j, source, "", conflict("rollback coordinator identity changed")
	}
	var inputRaw string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND requested_by=? AND kind='instance_rollback'", r.JobID, id, r.ActorID).Scan(&inputRaw); err != nil {
		return j, source, "", err
	}
	var input rollbackJobInput
	if json.Unmarshal([]byte(inputRaw), &input) != nil || input.PreviewID != p.ID || input.Confirmation != id || !input.AcknowledgeWorldRewind {
		return j, source, "", conflict("original persisted rollback job consent changed")
	}
	if err = s.config.DB.QueryRowContext(ctx, "SELECT consent_json FROM backup_update_rollbacks WHERE transaction_id=?", transaction).Scan(&consentRaw); err != nil {
		return j, source, "", err
	}
	var consent struct {
		Version     int                    `json:"version"`
		Receipt     backup.RollbackReceipt `json:"receipt"`
		WorldRewind bool                   `json:"world_rewind"`
	}
	if json.Unmarshal([]byte(consentRaw), &consent) != nil || consent.Version != 1 || !consent.WorldRewind || consent.Receipt != r {
		return j, source, "", conflict("backup consent differs from coordinator")
	}
	return j, source, parentJob, nil
}

func (c *rollbackCoordinator) restore(ctx context.Context, scope *instance.TransactionScope, j rollbackJournal, source rollbackSource, actor domain.User) error {
	s := c.service
	r := j.Receipt
	// This revalidation is also required on the original live job after admission.
	raw, _ := json.Marshal(j)
	if _, _, _, err := c.validateRecovery(ctx, r.InstanceID, r.TransactionID, string(raw)); err != nil {
		return err
	}
	var state, receiptState string
	if err := s.config.DB.QueryRowContext(ctx, "SELECT t.state,b.state FROM update_transactions t JOIN backup_update_rollbacks b ON b.transaction_id=t.id WHERE t.id=?", r.TransactionID).Scan(&state, &receiptState); err != nil {
		return err
	}
	committed := state == "committed" || state == "complete"
	if !committed && state != "prepared" && state != "publishing" {
		return conflict("rollback recovery phase cannot restore worlds")
	}
	expected := j.Preview.Snapshot.Generation
	beforeContent := j.CurrentContent
	beforePack := j.CurrentPack
	if committed {
		expected = r.TransactionID
		var err error
		_, beforeContent, beforePack, err = c.current(ctx, r.InstanceID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(beforePack, source.PackBefore) {
			return conflict("committed historical pack baseline changed")
		}
		if !sameRollbackItems(beforeContent.Items, source.ContentBefore.Items) {
			return conflict("committed historical content pointers changed")
		}
	}
	if err := launcher.MarkActivationPending(ctx, pathsDataDir(s.config.Paths, r.InstanceID), r.InstanceID); err != nil {
		return err
	}
	return s.config.Lifecycle.WithRollbackParticipant(ctx, scope, r.TransactionID, expected, source.Before, actor, func(executable *lifecycle.RollbackParticipant) error {
		next, err := executable.State()
		if err != nil {
			return err
		}
		if err = executable.Seal(ctx); err != nil {
			return err
		}
		var selectionCommit func(context.Context, *sql.Tx) error
		restoreFiles := func() error {
			return s.config.Packs.WithRollbackParticipant(ctx, scope, beforePack, source.PackBefore, actor, func(pack *modpack.RollbackParticipant) error {
				return s.config.Content.WithRollbackParticipant(ctx, scope, beforeContent, source.ContentBefore, executable, actor, func(items *content.RollbackParticipant) error {
					if committed && receiptState == "complete" {
						if err := items.ValidatePostimages(ctx); err != nil {
							return err
						}
						if err := scope.MaterializeRestoredProperties(ctx); err != nil {
							return err
						}
						if state == "complete" {
							_, _, parentJob, _, err := c.parent(ctx, r.InstanceID, r.ParentTransactionID)
							if err != nil {
								return err
							}
							return c.clear(ctx, j, parentJob)
						}
						return c.complete(ctx, j)
					}
					return s.config.Backups.WithUpdateRollbackRecovery(ctx, scope, r.TransactionID, func(worlds *backup.RollbackParticipant) error {
						if !committed {
							if err := worlds.Replace(ctx); err != nil {
								return err
							}
						}
						if err := items.ValidatePostimages(ctx); err != nil {
							return err
						}
						if !committed {
							if err := scope.WithCommit(ctx, func(operation context.Context, tx *sql.Tx) error {
								if err := items.Commit(operation, tx); err != nil {
									return err
								}
								if err := pack.Commit(operation, tx); err != nil {
									return err
								}
								if err := executable.Commit(operation, tx); err != nil {
									return err
								}
								if selectionCommit == nil {
									return conflict("rollback selection participant missing")
								}
								if err := selectionCommit(operation, tx); err != nil {
									return err
								}
								if err := worlds.Commit(operation, tx); err != nil {
									return err
								}
								result, err := tx.ExecContext(operation, "UPDATE update_transactions SET state='committed',updated_at=? WHERE id=? AND state IN ('prepared','publishing')", stamp(), r.TransactionID)
								if err != nil {
									return err
								}
								n, err := result.RowsAffected()
								if err != nil {
									return err
								}
								if n != 1 {
									return conflict("rollback pointer owner changed")
								}
								return nil
							}); err != nil {
								return err
							}
						}
						if err := scope.MaterializeRestoredProperties(ctx); err != nil {
							return err
						}
						if err := executable.Seal(ctx); err != nil {
							return err
						}
						if err := worlds.Finish(ctx); err != nil {
							return err
						}
						return c.complete(ctx, j)
					})
				})
			})
		}
		if committed {
			return restoreFiles()
		}
		return scope.WithSelectionParticipant(ctx, j.Preview.Snapshot.InstanceRevision, next.Plan.Selection, next.Runtime, actor, func(selection *instance.SelectionParticipant) error {
			selectionCommit = selection.Commit
			return restoreFiles()
		})
	})
}

// complete retains both independent blockers until the backup receipt and all
// pointer/materialization/seal work have finished durably. No restart occurs.
func (c *rollbackCoordinator) complete(ctx context.Context, j rollbackJournal) error {
	r := j.Receipt
	s := c.service
	result, err := s.config.DB.ExecContext(ctx, "UPDATE update_transactions SET state='complete',updated_at=? WHERE id=? AND state='committed' AND EXISTS(SELECT 1 FROM backup_update_rollbacks WHERE transaction_id=? AND state='complete')", stamp(), r.TransactionID, r.TransactionID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return conflict("rollback finalization ownership changed")
	}
	_, _, parentJob, _, err := c.parent(ctx, r.InstanceID, r.ParentTransactionID)
	if err != nil {
		return err
	}
	return c.clear(ctx, j, parentJob)
}
func (c *rollbackCoordinator) clear(ctx context.Context, j rollbackJournal, parentJob string) error {
	r := j.Receipt
	data := pathsDataDir(c.service.config.Paths, r.InstanceID)
	old := launcher.UpdateBlocker{Version: 1, InstanceID: r.InstanceID, TransactionID: r.ParentTransactionID, JobID: parentJob, Kind: "update"}
	next := launcher.UpdateBlocker{Version: 1, InstanceID: r.InstanceID, TransactionID: r.TransactionID, JobID: r.JobID, Kind: "instance_rollback"}
	if err := launcher.ClearActivationPending(ctx, data, r.InstanceID); err != nil {
		return err
	}
	if err := launcher.ClearWorldRiskBlocker(ctx, data, old); err != nil {
		return err
	}
	return launcher.ClearUpdateBlocker(ctx, data, next)
}

func sameRollbackItems(a, b []content.Installed) bool {
	clone := func(items []content.Installed) []content.Installed {
		raw, _ := json.Marshal(items)
		var out []content.Installed
		_ = json.Unmarshal(raw, &out)
		for i := range out {
			out[i].LanguageEvidence = nil
		}
		return out
	}
	return reflect.DeepEqual(clone(a), clone(b))
}
