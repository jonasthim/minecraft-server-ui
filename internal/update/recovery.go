package update

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

type recoveryTransaction struct {
	id, instance, kind, state, job, raw string
	actor                               int64
}

func decodeJournal(raw string, id string) (Journal, error) {
	var j Journal
	if len(raw) > 32<<20 || json.Unmarshal([]byte(raw), &j) != nil || j.Version != 1 || j.Plan.InstanceID != id || !validHex(j.Before.Generation, 32) || !validHex(j.After.Generation, 32) || j.Before.Generation == j.After.Generation || j.Before.Plan.Selection != j.Plan.From || j.After.Plan.Selection != j.Plan.Target || j.ContentBefore.Revision != j.Plan.Snapshot.ContentRevision || j.Before.Generation != j.Plan.Snapshot.Generation || j.Before.Revision != j.Plan.Snapshot.InstanceRevision {
		return j, conflict("update recovery journal is malformed or mismatched")
	}
	if len(j.Operations) > 16384 || len(j.ContentAfter) > 4096 {
		return j, invalid("update recovery bound exceeded")
	}
	return j, nil
}

// Reconcile runs while jobs/autostart are paused, before ordinary instance
// materialization. It never restarts a process or rewinds any world bytes.
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.config.DB.QueryContext(ctx, `SELECT id,instance_id,kind,state,job_id,actor_id,journal_json FROM update_transactions WHERE state NOT IN ('complete','rolled_back','superseded') OR (kind='update' AND state IN ('complete','rolled_back') AND json_valid(journal_json) AND json_extract(journal_json,'$.autostart.disable_confirmed')=1 AND coalesce(json_extract(journal_json,'$.autostart.restore_confirmed'),0)=0 AND EXISTS(SELECT 1 FROM lifecycle_installations l WHERE l.instance_id=update_transactions.instance_id AND l.generation=CASE WHEN state='complete' THEN json_extract(journal_json,'$.after.generation') ELSE json_extract(journal_json,'$.before.generation') END)) ORDER BY instance_id,id LIMIT 10001`)
	if err != nil {
		return err
	}
	pending := []recoveryTransaction{}
	for rows.Next() {
		var row recoveryTransaction
		if err = rows.Scan(&row.id, &row.instance, &row.kind, &row.state, &row.job, &row.actor, &row.raw); err != nil {
			break
		}
		pending = append(pending, row)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	if len(pending) > 10000 {
		return conflict("update recovery bound exceeded")
	}
	var failures []error
	for _, row := range pending {
		if row.kind == "instance_rollback" {
			if s.config.Rollbacks == nil {
				failures = append(failures, conflict("explicit rollback recovery capability unavailable"))
				continue
			}
			if err = s.config.Rollbacks.Reconcile(ctx, row.instance, row.id, json.RawMessage(row.raw)); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		if err = s.reconcileNormal(ctx, row); err != nil {
			failures = append(failures, err)
		}
	}
	// Independent blockers without a matching active row remain blocking. Terminal
	// rows interrupted during marker cleanup may be safely sealed and cleared.
	rows, err = s.config.DB.QueryContext(ctx, "SELECT id FROM instances ORDER BY id")
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return errors.Join(append(failures, err)...)
	}
	for _, id := range ids {
		data, e := s.dataDir(id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		marker, present, e := launcher.ReadUpdateBlocker(ctx, data, id)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if !present {
			risk, riskPresent, riskErr := launcher.ReadWorldRiskBlocker(ctx, data, id)
			if riskErr != nil {
				failures = append(failures, riskErr)
				continue
			}
			if !riskPresent {
				continue
			}
			var successor string
			if e = s.config.DB.QueryRowContext(ctx, "SELECT coalesce(superseded_by,'') FROM update_transactions WHERE id=? AND instance_id=?", risk.TransactionID, id).Scan(&successor); e != nil || successor == "" {
				continue
			}
			var kind, state, raw string
			if e = s.config.DB.QueryRowContext(ctx, "SELECT kind,state,journal_json FROM update_transactions WHERE id=? AND instance_id=?", successor, id).Scan(&kind, &state, &raw); e != nil {
				failures = append(failures, e)
				continue
			}
			if kind != "instance_rollback" || state != "complete" || s.config.Rollbacks == nil {
				failures = append(failures, conflict("world-risk successor is not a completed consented rollback"))
				continue
			}
			if kind == "instance_rollback" && state == "complete" && s.config.Rollbacks != nil {
				if e = s.config.Rollbacks.Reconcile(ctx, id, successor, json.RawMessage(raw)); e != nil {
					failures = append(failures, e)
				}
			}
			continue
		}
		var row recoveryTransaction
		row.instance = id
		row.id = marker.TransactionID
		e = s.config.DB.QueryRowContext(ctx, "SELECT kind,state,job_id,actor_id,journal_json FROM update_transactions WHERE id=? AND instance_id=?", row.id, id).Scan(&row.kind, &row.state, &row.job, &row.actor, &row.raw)
		if e != nil {
			failures = append(failures, conflict("orphan update marker requires repair"))
			continue
		}
		if row.kind == "instance_rollback" && row.state == "complete" && s.config.Rollbacks != nil {
			if e = s.config.Rollbacks.Reconcile(ctx, id, row.id, json.RawMessage(row.raw)); e != nil {
				failures = append(failures, e)
			}
		}
		if row.kind == "update" && (row.state == "complete" || row.state == "rolled_back") {
			if e = s.reconcileNormal(ctx, row); e != nil {
				failures = append(failures, e)
			}
		}
	}
	return errors.Join(failures...)
}
func (s *Service) reconcileNormal(ctx context.Context, row recoveryTransaction) error {
	j, err := decodeJournal(row.raw, row.instance)
	if err != nil {
		return err
	}
	data, err := s.dataDir(row.instance)
	if err != nil {
		return err
	}
	marker := launcher.UpdateBlocker{Version: 1, InstanceID: row.instance, TransactionID: row.id, JobID: row.job, Kind: "update"}
	// A persisted boot intent is sufficient even if the crash preceded native.Start
	// or independent risk-marker publication. Recovery never guesses world age.
	if j.BootAttempted && row.state != "complete" {
		if err = launcher.WriteWorldRiskBlocker(ctx, data, marker); err != nil {
			return err
		}
	}
	if j.BootAttempted && row.state != "complete" {
		// A game may have changed world/addon/config bytes. No postboot replay,
		// materialization or reseal is allowed, including with a surviving JVM.
		if err = s.checkOrdinaryReady(ctx, row.instance); err != nil {
			return err
		}
		if err = s.verifyNormalPointers(ctx, row.instance, j, true); err != nil {
			return err
		}
		if err = launcher.WriteUpdateBlocker(ctx, data, marker); err != nil {
			return err
		}
		if err = launcher.MarkActivationPending(ctx, data, row.instance); err != nil {
			return err
		}
		if j.Autostart == nil {
			return conflict("attempted update lacks native autostart suspension intent")
		}
		policy, err := s.config.Lifecycle.ReassertUpdateAutostart(ctx, row.instance, row.id)
		if err != nil {
			return err
		}
		j.Autostart = &policy
		if err = s.setJournalState(ctx, row.id, j, "blocked"); err != nil {
			return err
		}
		observation, err := s.config.Lifecycle.RecoverUpdateProcess(ctx, row.instance, row.id)
		if err != nil {
			return err
		}
		j.RecoveryObservation = observation
		return s.setJournalState(ctx, row.id, j, "blocked")
	}
	if (row.state == "complete" || row.state == "rolled_back") && j.Autostart != nil {
		_, present, err := launcher.ReadUpdateBlocker(ctx, data, row.instance)
		if err != nil {
			return err
		}
		if !present {
			if err = s.verifyNormalPointers(ctx, row.instance, j, row.state == "complete"); err != nil {
				return err
			}
			// Pointer finalization already succeeded before the interrupted
			// policy repair. Never rematerialize a potentially running game.
			policy, restoreErr := s.config.Lifecycle.RestoreRecoveredUpdateAutostart(ctx, row.instance, row.id)
			if restoreErr != nil {
				return nil
			}
			j.Autostart = &policy
			return s.setJournalState(ctx, row.id, j, row.state)
		}
	}
	if j.Autostart != nil {
		policy, err := s.config.Lifecycle.ReassertUpdateAutostart(ctx, row.instance, row.id)
		if err != nil {
			return err
		}
		j.Autostart = &policy
		if err = s.setJournalState(ctx, row.id, j, row.state); err != nil {
			return err
		}
	}
	finalState := "rolled_back"
	result := s.config.Instances.WithStoppedRecovery(ctx, row.instance, func(session *instance.StoppedSession) error {
		if err := s.checkOrdinaryReady(ctx, row.instance); err != nil {
			return err
		}
		current, err := s.config.Lifecycle.UpdateSnapshot(ctx, row.instance)
		if err != nil {
			return err
		}
		forward := current.Generation == j.After.Generation
		if !forward && current.Generation != j.Before.Generation {
			return conflict("update recovery found a foreign lifecycle generation")
		}
		if j.BootAttempted && !forward {
			return conflict("boot-attempted update lost NEW pointers; explicit repair required")
		}
		if row.state == "complete" && !forward || row.state == "rolled_back" && forward {
			return conflict("terminal update pointers disagree with recovery history")
		}
		if err = launcher.WriteUpdateBlocker(ctx, data, marker); err != nil {
			return err
		}
		if err = launcher.MarkActivationPending(ctx, data, row.instance); err != nil {
			return err
		}
		return session.WithCoordinatorScope(ctx, func(operation context.Context, scope *instance.TransactionScope) error {
			// Check SQL ownership before any file publication/materialization. Recovery
			// must not allow ordinary materialization to overwrite changed postimages.
			if err := s.verifyNormalPointers(operation, row.instance, j, forward); err != nil {
				return err
			}
			if err := s.restoreNormalFiles(operation, scope, row.instance, row.id, j, forward); err != nil {
				return err
			}
			if forward {
				finalState = "complete"
			}
			return nil
		})
	})
	if result != nil {
		return result
	}
	if j.Autostart != nil {
		if policy, restoreErr := s.config.Lifecycle.RestoreRecoveredUpdateAutostart(ctx, row.instance, row.id); restoreErr == nil {
			j.Autostart = &policy
		}
	}
	if err = s.setJournalState(ctx, row.id, j, finalState); err != nil {
		return err
	}
	if err = launcher.ClearActivationPending(ctx, data, row.instance); err != nil {
		return err
	}
	return launcher.ClearUpdateBlocker(ctx, data, marker)
}
