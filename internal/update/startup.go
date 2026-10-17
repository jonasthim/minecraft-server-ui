package update

import (
	"context"
	"database/sql"
	"reflect"

	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// StartupBlocked distinguishes a fully reconciled world-risk administration hold from
// corruption or unfinished publication. It never grants ordinary mutation or
// start authority and never writes any file or row. A surviving JVM can remain
// running or unknown without authorizing signals or data/configuration writes.
func (s *Service) StartupBlocked(ctx context.Context, id string) (bool, error) {
	if err := s.CheckReady(ctx, id); err == nil {
		return false, nil
	}
	var row recoveryTransaction
	row.instance = id
	err := s.config.DB.QueryRowContext(ctx, `SELECT id,kind,state,job_id,actor_id,journal_json FROM update_transactions WHERE instance_id=? AND state NOT IN ('complete','rolled_back','superseded')`, id).Scan(&row.id, &row.kind, &row.state, &row.job, &row.actor, &row.raw)
	if err == sql.ErrNoRows {
		return false, conflict("orphan update or world-risk marker requires repair")
	}
	if err != nil {
		return false, err
	}
	if row.kind != "update" || row.state != "blocked" {
		return false, conflict("update publication is not safely reconciled")
	}
	j, err := decodeJournal(row.raw, id)
	if err != nil {
		return false, err
	}
	if !j.BootAttempted {
		return false, conflict("update block is not a reconciled world-risk hold")
	}
	if err = s.checkOrdinaryReady(ctx, id); err != nil {
		return false, err
	}
	if err = s.verifyNormalPointers(ctx, id, j, true); err != nil {
		return false, err
	}
	if j.Autostart == nil || !j.Autostart.DisableConfirmed || j.Autostart.RestoreConfirmed {
		return false, conflict("world-risk autostart suspension is not confirmed")
	}
	var applied bool
	if err = s.config.DB.QueryRowContext(ctx, "SELECT coalesce(applied,1) FROM lifecycle_autostart WHERE instance_id=?", id).Scan(&applied); err != nil {
		return false, err
	}
	if applied {
		return false, conflict("world-risk native policy is not suspended")
	}
	data, err := s.dataDir(id)
	if err != nil {
		return false, err
	}
	expected := launcher.UpdateBlocker{Version: 1, InstanceID: id, TransactionID: row.id, JobID: row.job, Kind: "update"}
	marker, present, err := launcher.ReadUpdateBlocker(ctx, data, id)
	if err != nil {
		return false, err
	}
	if !present || !reflect.DeepEqual(marker, expected) {
		return false, conflict("reconciled update marker differs")
	}
	risk, present, err := launcher.ReadWorldRiskBlocker(ctx, data, id)
	if err != nil {
		return false, err
	}
	if !present || !reflect.DeepEqual(risk, expected) {
		return false, conflict("reconciled world-risk marker differs")
	}
	pending, err := launcher.UpdateActivationPending(ctx, data, id)
	if err != nil {
		return false, err
	}
	if !pending {
		return false, conflict("protected world-risk activation blocker is absent")
	}
	return true, nil
}
func (s *Service) verifyJournalFiles(ctx context.Context, id string, j Journal) error {
	d, err := s.config.Paths.For(id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, d.Server)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, op := range j.Operations {
		if !journalPath(op.Path) {
			return invalid("unsafe update postimage path")
		}
		if _, err = s.config.Paths.Resolve(id, "server", op.Path); err != nil {
			return err
		}
		hash, err := content.PackCurrentHash(ctx, root, op.Path, content.MaxArtifactBytes)
		if err != nil {
			return err
		}
		if hash != op.After {
			return conflict("reconciled update postimage differs")
		}
	}
	return nil
}
