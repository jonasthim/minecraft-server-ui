package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

func (s *Service) currentSnapshot(ctx context.Context, id string) (Snapshot, error) {
	current, err := s.config.Lifecycle.UpdateSnapshot(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	manifest, err := s.config.Content.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	pack, err := s.config.Packs.Snapshot(ctx, id)
	if err != nil {
		return Snapshot{}, err
	}
	revision := "none"
	if pack.Baseline != nil {
		revision = pack.Baseline.Revision
	}
	return Snapshot{current.Revision, current.Generation, manifest.Revision, revision}, nil
}
func (s *Service) EnqueueApply(ctx context.Context, id, preview string, confirmed bool, actor domain.User) (jobs.Job, error) {
	fresh, err := s.actor(ctx, actor.ID, false)
	if err != nil {
		return jobs.Job{}, err
	}
	envelope, err := s.loadPreview(ctx, id, preview, fresh.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	needsAdmin, err := s.PreviewNeedsAdministrator(ctx, id, envelope.Request, fresh.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if needsAdmin && fresh.Role != domain.RoleAdmin {
		return jobs.Job{}, domain.E(domain.CodeForbidden, "manual update receipts require current administrator authority")
	}
	frozen, err := Freeze(envelope.Plan)
	if err != nil {
		return jobs.Job{}, err
	}
	snapshot, err := s.currentSnapshot(ctx, id)
	if err != nil {
		return jobs.Job{}, err
	}
	if err = frozen.CheckApply(snapshot, fresh.ID, time.Now(), confirmed); err != nil {
		return jobs.Job{}, err
	}
	if err = s.checkAdmissionReady(ctx, id); err != nil {
		return jobs.Job{}, err
	}
	raw, _ := json.Marshal(jobInput{Action: "apply", PreviewID: preview, Confirmed: confirmed})
	if err = s.audit(ctx, id, fresh, "apply_request", "confirmed"); err != nil {
		return jobs.Job{}, err
	}
	return s.config.Jobs.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "instance_update", Payload: raw, RequestedBy: fresh.ID, Cancellable: true})
}

// EnqueueAutomaticOnce is the scheduler's immutable adapter. Targets are derived
// by the current job, never injected by the scheduler or an idempotent replay.
func (s *Service) EnqueueAutomaticOnce(ctx context.Context, key, id string, actor domain.User) (jobs.Job, error) {
	fresh, err := s.actor(ctx, actor.ID, false)
	if err != nil {
		return jobs.Job{}, err
	}
	return s.config.Jobs.EnqueueIdempotent(ctx, key, jobs.Request{InstanceID: id, Kind: "instance_update", Payload: []byte(`{"action":"automatic"}`), RequestedBy: fresh.ID, Cancellable: true})
}
func (s *Service) runApply(ctx context.Context, e *jobs.Execution) (result error) {
	current, actor, err := s.execution(ctx, e)
	if err != nil {
		return err
	}
	if current.Kind != "instance_update" {
		return invalid("typed update execution required")
	}
	// Use durable input rather than mutable public execution fields.
	var raw string
	if err = s.config.DB.QueryRowContext(ctx, "SELECT payload FROM jobs WHERE id=? AND instance_id=? AND kind='instance_update' AND state='running'", current.JobID, current.InstanceID).Scan(&raw); err != nil {
		return err
	}
	var input jobInput
	if json.Unmarshal([]byte(raw), &input) != nil {
		return invalid("malformed update request")
	}
	var prepared *preparedTransaction
	switch input.Action {
	case "automatic":
		prepared, err = s.prepare(ctx, e, Request{Mode: Automatic}, actor)
	case "apply":
		envelope, loadErr := s.loadPreview(ctx, current.InstanceID, input.PreviewID, actor.ID)
		if loadErr != nil {
			return loadErr
		}
		snapshot, snapshotErr := s.currentSnapshot(ctx, current.InstanceID)
		if snapshotErr != nil {
			return snapshotErr
		}
		frozen, freezeErr := Freeze(envelope.Plan)
		if freezeErr != nil {
			return freezeErr
		}
		if err = frozen.CheckApply(snapshot, actor.ID, time.Now(), input.Confirmed); err != nil {
			return err
		}
		prepared, err = s.prepare(ctx, e, envelope.Request, actor)
		if err != nil {
			return err
		}
		prepared.plan.ID = envelope.Plan.ID
		prepared.plan.ExpiresAt = envelope.Plan.ExpiresAt
		fresh, freezeErr := Freeze(prepared.plan)
		if freezeErr != nil {
			return freezeErr
		}
		if fresh.Digest() != frozen.Digest() {
			return conflict("target evidence or proposed changes changed; create another preview")
		}
	default:
		return invalid("explicit typed update action required")
	}
	if err != nil {
		return err
	}
	if len(prepared.plan.Blockers) > 0 {
		return conflict("strict target compatibility or unresolved conflicts block update")
	}
	if len(prepared.plan.Changes) == 0 {
		return e.Log(ctx, "Pinned instance already matches verified update targets; no activation needed.")
	}
	transaction, err := opaqueID()
	if err != nil {
		return err
	}
	journal, err := s.stageJournal(ctx, current.InstanceID, transaction, prepared)
	if err != nil {
		return err
	}
	if err = e.Phase(ctx, "update_safety_backup", 25); err != nil {
		return err
	}
	safety, err := s.config.Backups.CreateForJob(ctx, e, domain.BackupFull, domain.BackupSafety)
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "Update blocked because full safety backup failed", err)
	}
	if safety.State != domain.BackupReady || safety.Kind != domain.BackupFull || !safety.Protected {
		return conflict("ready protected full backup required")
	}
	epoch, guard, err := s.config.BeginUpdateStop(ctx, e, actor)
	if err != nil {
		return err
	}
	journal.StopEpoch = epoch
	wasRunning, err := s.config.Lifecycle.StopForUpdate(ctx, e, actor, prepared.plan.CountdownSeconds)
	if err != nil {
		return err
	}
	journal.WasRunning = wasRunning
	data, err := s.dataDir(current.InstanceID)
	if err != nil {
		return err
	}
	marker := launcher.UpdateBlocker{Version: 1, InstanceID: current.InstanceID, TransactionID: transaction, JobID: current.JobID, Kind: "update"}
	admitted := false
	defer func() {
		if result != nil && admitted {
			cleanup, cancel := e.CleanupContext()
			defer cancel()
			if journal.BootAttempted {
				_ = launcher.WriteWorldRiskBlocker(cleanup, data, marker)
				_ = launcher.WriteUpdateBlocker(cleanup, data, marker)
				_ = launcher.MarkActivationPending(cleanup, data, current.InstanceID)
			}
			encoded, encodeErr := journalJSON(journal)
			if encodeErr == nil {
				_, _ = s.config.DB.ExecContext(cleanup, "UPDATE update_transactions SET state='blocked',journal_json=?,updated_at=? WHERE id=? AND state NOT IN ('complete','rolled_back','superseded')", encoded, stamp(), transaction)
			}
			_ = s.audit(cleanup, current.InstanceID, actor, "apply_result", "blocked")
		}
	}()

	if err := s.checkAdmissionReady(ctx, current.InstanceID); err != nil {
		return err
	}
	now, err := s.currentSnapshot(ctx, current.InstanceID)
	if err != nil {
		return err
	}
	if now != prepared.plan.Snapshot {
		return conflict("instance changed before stopped update admission")
	}
	journal.Autostart, err = s.autostartSnapshot(ctx, current.InstanceID)
	if err != nil {
		return err
	}
	encoded, err := journalJSON(journal)
	if err != nil {
		return err
	}
	if _, err = s.config.DB.ExecContext(ctx, `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,?,?,?,?,'update','prepared',?,?,?)`, transaction, current.InstanceID, prepared.plan.ID, current.JobID, actor.ID, safety.ID, encoded, stamp()); err != nil {
		return err
	}
	admitted = true
	if err = launcher.WriteUpdateBlocker(ctx, data, marker); err != nil {
		return err
	}
	if err = launcher.MarkActivationPending(ctx, data, current.InstanceID); err != nil {
		return err
	}
	policy, err := s.config.Lifecycle.SuspendUpdateAutostart(ctx, e, actor, transaction)
	if err != nil {
		return err
	}
	journal.Autostart = &policy
	if err = s.setJournalState(ctx, transaction, journal, "prepared"); err != nil {
		return err
	}
	err = s.config.Instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
		if err := s.CheckJobReady(ctx, e, current.InstanceID); err != nil {
			return err
		}
		return session.WithCoordinatorScope(ctx, func(operation context.Context, scope *instance.TransactionScope) (scopeErr error) {
			committed := false
			defer func() {
				if scopeErr != nil && !committed && operation.Err() == nil {
					restoreErr := s.restoreNormalFiles(operation, scope, current.InstanceID, transaction, journal, false)
					if restoreErr == nil {
						restoreErr = s.setJournalState(operation, transaction, journal, "rolled_back")
					}
					if restoreErr == nil {
						restoreErr = launcher.ClearActivationPending(operation, data, current.InstanceID)
					}
					if restoreErr == nil {
						restoreErr = launcher.ClearUpdateBlocker(operation, data, marker)
					}
					scopeErr = errors.Join(scopeErr, restoreErr)
				}
			}()
			participantErr := s.config.Lifecycle.WithUpdateParticipant(operation, scope, prepared.lifecycle, transaction, func(native *lifecycle.UpdateParticipant) error {
				return scope.WithSelectionParticipant(operation, prepared.before.Revision, prepared.after.Plan.Selection, prepared.after.Runtime, actor, func(selection *instance.SelectionParticipant) error {
					return s.config.Packs.WithUpdateParticipant(operation, scope, prepared.pack, actor, func(pack *modpack.UpdateParticipant) error {
						return s.config.Content.WithUpdateParticipant(operation, scope, prepared.content, actor, func(content *content.UpdateParticipant) error {
							if err := s.setJournalState(operation, transaction, journal, "publishing"); err != nil {
								return err
							}
							if err := scope.WithUse(operation, func(ctx context.Context, _ instance.ScopeSnapshot) error {
								return s.applyJournalFiles(ctx, current.InstanceID, transaction, journal, true)
							}); err != nil {
								return err
							}
							if err := pack.ValidatePostimages(operation); err != nil {
								return err
							}
							if err := content.ValidatePostimages(operation); err != nil {
								return err
							}
							if err := native.Seal(operation); err != nil {
								return err
							}
							if err := scope.WithCommit(operation, func(ctx context.Context, tx *sql.Tx) error {
								if err := content.Commit(ctx, tx); err != nil {
									return err
								}
								if err := pack.Commit(ctx, tx); err != nil {
									return err
								}
								if err := native.Commit(ctx, tx); err != nil {
									return err
								}
								if err := selection.Commit(ctx, tx); err != nil {
									return err
								}
								encoded, err := journalJSON(journal)
								if err != nil {
									return err
								}
								_, err = tx.ExecContext(ctx, "UPDATE update_transactions SET state='committed',journal_json=?,updated_at=? WHERE id=? AND state='publishing'", encoded, stamp(), transaction)
								return err
							}); err != nil {
								return err
							}
							committed = true
							return nil
						})
					})
				})
			})
			if participantErr != nil {
				return participantErr
			}
			if err := scope.Materialize(operation); err != nil {
				return err
			}
			if err := s.config.Lifecycle.SealCommittedUpdate(operation, scope); err != nil {
				return err
			}
			if err := s.verifyNormalPointers(operation, current.InstanceID, journal, true); err != nil {
				return err
			}
			// Intent is durable while both publication blockers are still present.
			if wasRunning {
				journal.BootAttempted = true
				if err := s.setJournalState(operation, transaction, journal, "boot_attempted"); err != nil {
					return err
				}
			}
			if err := launcher.ClearActivationPending(operation, data, current.InstanceID); err != nil {
				return err
			}
			return launcher.ClearUpdateBlocker(operation, data, marker)
		})
	})
	if err != nil {
		if !journal.BootAttempted {
			var state string
			_ = s.config.DB.QueryRowContext(ctx, "SELECT state FROM update_transactions WHERE id=?", transaction).Scan(&state)
			if state == "rolled_back" {
				if policy, restoreErr := s.config.Lifecycle.RestoreUpdateAutostart(ctx, e, actor, transaction); restoreErr == nil {
					journal.Autostart = &policy
					_ = s.setJournalState(ctx, transaction, journal, "rolled_back")
				}
			}
		}
		return err
	}
	if wasRunning {
		if err = s.config.Lifecycle.StartForUpdateWithReceipt(ctx, e, actor, guard, func(ctx context.Context, identity domain.ProcessIdentity) error {
			journal.StartedIdentity = &identity
			return s.setJournalState(ctx, transaction, journal, "boot_attempted")
		}); err != nil {
			return err
		}
	}
	policy, restoreErr := s.config.Lifecycle.RestoreUpdateAutostart(ctx, e, actor, transaction)
	if restoreErr == nil {
		journal.Autostart = &policy
	} else {
		if err = e.Log(ctx, "Update pointers and readiness verified, but native autostart remains suspended; inspect server operations and re-enable explicitly."); err != nil {
			return err
		}
	}
	if err = s.audit(ctx, current.InstanceID, actor, "apply_result", "complete"); err != nil {
		return err
	}
	if err = s.setJournalState(ctx, transaction, journal, "complete"); err != nil {
		return err
	}
	return e.Log(ctx, "Update committed with verified executable, content and configuration pointers. Safety backup retained; world data was not rewound.")
}
func (s *Service) setJournalState(ctx context.Context, transaction string, j Journal, state string) error {
	encoded, err := journalJSON(j)
	if err != nil {
		return err
	}
	_, err = s.config.DB.ExecContext(ctx, "UPDATE update_transactions SET state=?,journal_json=?,updated_at=? WHERE id=? AND state!='superseded'", state, encoded, stamp(), transaction)
	return err
}
func (s *Service) restoreNormalFiles(ctx context.Context, scope *instance.TransactionScope, id, transaction string, j Journal, forward bool) error {
	if !forward && j.BootAttempted {
		return conflict("automatic executable rollback forbidden after boot intent")
	}
	if err := scope.WithUse(ctx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		return s.applyJournalFiles(operation, id, transaction, j, forward)
	}); err != nil {
		return err
	}
	if err := scope.Materialize(ctx); err != nil {
		return err
	}
	return s.config.Lifecycle.SealCommittedUpdate(ctx, scope)
}
func (s *Service) verifyNormalPointers(ctx context.Context, id string, j Journal, forward bool) error {
	expected := j.Before
	items := j.ContentBefore.Items
	baseline := j.PackBefore
	if forward {
		expected = j.After
		items = j.ContentAfter
		baseline = j.PackAfter
	}
	actual, err := s.config.Lifecycle.UpdateSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if actual.Generation != expected.Generation || actual.Plan.Selection != expected.Plan.Selection || !reflect.DeepEqual(actual.Spec, expected.Spec) || actual.Runtime.Artifact.Checksum != expected.Runtime.Artifact.Checksum {
		return conflict("update lifecycle pointers disagree with committed journal")
	}
	manifest, err := s.config.Content.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(manifest.Items, items) {
		return conflict("update content pointers disagree with committed journal")
	}
	pack, err := s.config.Packs.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(pack.Baseline, baseline) {
		return conflict("update pack pointer disagrees with committed journal")
	}
	return nil
}
