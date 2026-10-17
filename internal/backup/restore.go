package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

const RestorePreserves = "Restore keeps current manager settings, ports, EULA acceptance, Minecraft/flavor/runtime selection and private management credentials. It restores selected local saved data, addons and ordinary unknown configuration. Matching version, flavor, loader, level-name and layout are required."

type restoreMove struct {
	Name      string `json:"name"`
	OldDigest string `json:"old_digest"`
	NewDigest string `json:"new_digest"`
	Phase     string `json:"phase"`
}
type restoreJournal struct {
	DataReplaced bool          `json:"data_replaced"`
	OperationID  string        `json:"operation_id"`
	BackupID     string        `json:"backup_id"`
	SafetyID     string        `json:"safety_id"`
	JobID        string        `json:"job_id"`
	InstanceID   string        `json:"instance_id"`
	Manifest     Manifest      `json:"manifest"`
	Moves        []restoreMove `json:"moves"`
}
type restoreMarker struct {
	OperationID string `json:"operation_id"`
	BackupID    string `json:"backup_id"`
}

func (s *Service) runRestore(ctx context.Context, e *jobs.Execution) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	defer func() {
		if result != nil {
			result = errors.Join(result, logFailure(e, result))
		}
	}()
	if err := e.Phase(ctx, "verifying_restore", 5); err != nil {
		return err
	}
	var input restoreRequest
	if json.Unmarshal(e.Payload, &input) != nil || !input.Confirmed || !validKey(input.BackupID) {
		return domain.E(domain.CodeValidationFailed, "restore requires explicit confirmed archive ID")
	}
	return s.WithStoppedJob(ctx, e, func(c *StoppedCapture, session *instance.StoppedSession) (result error) {
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			outcome := "restored"
			if result != nil {
				outcome = "failed_or_partial"
			}
			result = errors.Join(result, s.audit(cleanup, c.id, c.actor, "restore_result", input.BackupID, outcome))
		}()
		initialDirs, err := session.Paths(ctx)
		if err != nil {
			return err
		}
		if err = s.restorePreconditions(ctx, initialDirs, c.id); err != nil {
			return err
		}
		download, err := s.Download(ctx, c.id, input.BackupID, c.actor)
		if err != nil {
			return err
		}
		defer download.Close()
		record, err := s.record(ctx, c.id, input.BackupID)
		if err != nil {
			return err
		}
		manifest, err := validateArchive(ctx, download.File, record.SizeBytes, defaultLimits)
		if err != nil {
			return err
		}
		detail, err := session.Detail(ctx)
		if err != nil {
			return err
		}
		discovery, err := s.worlds.Discover(ctx, c.id)
		if err != nil {
			return err
		}
		if manifest.InstanceID != c.id || manifest.Version != detail.Instance.MinecraftVersion || manifest.Flavor != detail.Instance.Flavor || manifest.Loader != detail.Instance.LoaderVersion || manifest.Layout != discovery.Layout || manifest.LevelName != detail.Config.LevelName || discovery.State == "ambiguous" || discovery.State == "unsupported" {
			return domain.E(domain.CodeConflict, "restore archive version, flavor, loader, level-name or layout differs from current server")
		}
		if err = s.failAt("safety"); err != nil {
			return err
		}
		safety, err := c.Capture(ctx, domain.BackupFull, domain.BackupSafety)
		if err != nil {
			return domain.Wrap(domain.CodeConflict, "safety backup failed; restore data was not changed", err)
		}
		if safety.State != domain.BackupReady {
			return domain.E(domain.CodeConflict, "safety backup was not validated")
		}
		operationID, err := key()
		if err != nil {
			return err
		}
		dirs, err := session.Paths(ctx)
		if err != nil {
			return err
		}
		journal, err := s.stageRestoreJournal(ctx, dirs, download.File, record.SizeBytes, manifest, operationID, input.BackupID, safety.ID, c.job, c.id)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(journal)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO backup_restores(id,instance_id,backup_id,safety_id,job_id,state,journal_json,updated_at)VALUES(?,?,?,?,?,'prepared',?,?)`, operationID, c.id, input.BackupID, safety.ID, c.job, string(raw), stamp()); err != nil {
			return err
		}
		if err = s.writeRestoreMarker(ctx, dirs, journal); err != nil {
			return err
		}
		if err = s.failAt("restore_prepared"); err != nil {
			return err
		}
		return s.finishRestore(ctx, session, dirs, &journal)
	})
}
func (s *Service) writeRestoreMarker(ctx context.Context, dirs instance.Directories, journal restoreJournal) error {
	state, err := openSource(dirs.State)
	if err != nil {
		return err
	}
	defer state.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, _ := json.Marshal(restoreMarker{journal.OperationID, journal.BackupID})
	file, err := state.root.OpenFile("backup-restore.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	return syncDir(state.root, ".")
}
func (s *Service) journalRestore(ctx context.Context, journal *restoreJournal, state string) error {
	raw, _ := json.Marshal(journal)
	_, err := s.db.ExecContext(ctx, `UPDATE backup_restores SET state=?,journal_json=?,updated_at=? WHERE id=? AND instance_id=?`, state, string(raw), stamp(), journal.OperationID, journal.InstanceID)
	return err
}
func (s *Service) finishRestore(ctx context.Context, session *instance.StoppedSession, dirs instance.Directories, journal *restoreJournal) (err error) {
	defer func() {
		if err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.journalRestore(cleanup, journal, "manual_repair")
		}
	}()
	state, err := openSource(dirs.State)
	if err != nil {
		return err
	}
	defer state.Close()
	if err = verifyRestoreMarker(state, journal); err != nil {
		return err
	}
	if err = s.replaceRestoreMoves(ctx, dirs, journal, s.journalRestore); err != nil {
		return err
	}
	// Materialization may change server.properties and eula.txt from the archive;
	// record data replacement before this separately recoverable finalization.
	journal.DataReplaced = true
	if err = s.journalRestore(ctx, journal, "materializing"); err != nil {
		return err
	}
	if err = s.failAt("restore_materialize"); err != nil {
		return err
	}
	if err = session.MaterializeRestoredProperties(ctx); err != nil {
		return err
	}
	if err = state.verify(); err != nil {
		return err
	}
	if err = verifyRestoreMarker(state, journal); err != nil {
		return err
	}
	if err = s.journalRestore(ctx, journal, "committed"); err != nil {
		return err
	}
	if err = state.root.Remove("backup-restore.json"); err != nil {
		return err
	}
	if err = syncDir(state.root, "."); err != nil {
		return err
	}
	if err = s.journalRestore(ctx, journal, "complete"); err != nil {
		return err
	}
	return nil
}

// stageRestoreJournal validates/extracts into private staging and records exact
// old/new digests. It neither changes live files nor creates restore authority.
func (s *Service) stageRestoreJournal(ctx context.Context, dirs instance.Directories, file *os.File, size int64, manifest Manifest, operationID, backupID, safetyID, jobID, instanceID string) (restoreJournal, error) {
	backups, err := openSource(dirs.Backups)
	if err != nil {
		return restoreJournal{}, err
	}
	defer backups.Close()
	opDir := ".restore-" + operationID
	if err = backups.root.Mkdir(opDir, 0700); err != nil {
		return restoreJournal{}, err
	}
	operation, err := openSource(filepath.Join(dirs.Backups, opDir))
	if err != nil {
		return restoreJournal{}, err
	}
	defer operation.Close()
	for _, name := range []string{"new", "preimage"} {
		if err = operation.root.Mkdir(name, 0700); err != nil {
			return restoreJournal{}, err
		}
	}
	stage, err := openSource(filepath.Join(dirs.Backups, opDir, "new"))
	if err != nil {
		return restoreJournal{}, err
	}
	defer stage.Close()
	if _, err = extractArchive(ctx, file, size, stage, defaultLimits); err != nil {
		return restoreJournal{}, err
	}
	for _, root := range manifest.Roots {
		if _, err := stage.root.Lstat(root); os.IsNotExist(err) {
			if err = stage.root.Mkdir(root, 0700); err != nil {
				return restoreJournal{}, err
			}
		}
	}
	if err = syncTree(ctx, stage.root, 0); err != nil {
		return restoreJournal{}, err
	}
	if err = errors.Join(syncDir(operation.root, "."), syncDir(backups.root, ".")); err != nil {
		return restoreJournal{}, err
	}
	server, err := openSource(dirs.Server)
	if err != nil {
		return restoreJournal{}, err
	}
	defer server.Close()
	names := append([]string(nil), manifest.Roots...)
	if manifest.Kind == domain.BackupFull {
		entries, err := boundedReadDir(server.root, defaultLimits.entries)
		if err != nil {
			return restoreJournal{}, err
		}
		for _, entry := range entries {
			if !excluded(entry.Name()) {
				names = append(names, entry.Name())
			}
		}
	}
	sort.Strings(names)
	journal := restoreJournal{OperationID: operationID, BackupID: backupID, SafetyID: safetyID, JobID: jobID, InstanceID: instanceID, Manifest: manifest, Moves: []restoreMove{}}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[strings.ToLower(name)] {
			continue
		}
		if safePath(name) != nil || strings.Contains(name, "/") || excluded(name) {
			return restoreJournal{}, errCorrupt
		}
		seen[strings.ToLower(name)] = true
		oldDigest, _, err := treeDigest(ctx, server.root, name)
		if err != nil {
			return restoreJournal{}, err
		}
		newDigest, _, err := treeDigest(ctx, stage.root, name)
		if err != nil {
			return restoreJournal{}, err
		}
		journal.Moves = append(journal.Moves, restoreMove{name, oldDigest, newDigest, "pending"})
	}
	return journal, nil
}

// replaceRestoreMoves is the shared digest/CAS/rename/sync engine. Its caller
// owns consent, marker verification and the durable journal namespace. It does
// not materialize configuration, change manager pointers or clear a blocker.
func (s *Service) replaceRestoreMoves(ctx context.Context, dirs instance.Directories, journal *restoreJournal, checkpoint func(context.Context, *restoreJournal, string) error) error {
	server, err := openSource(dirs.Server)
	if err != nil {
		return err
	}
	defer server.Close()
	opPath := filepath.Join(dirs.Backups, ".restore-"+journal.OperationID)
	stage, err := openSource(filepath.Join(opPath, "new"))
	if err != nil {
		return err
	}
	defer stage.Close()
	preimage, err := openSource(filepath.Join(opPath, "preimage"))
	if err != nil {
		return err
	}
	defer preimage.Close()
	for i := range journal.Moves {
		move := &journal.Moves[i]
		if safePath(move.Name) != nil || strings.Contains(move.Name, "/") || excluded(move.Name) {
			return errCorrupt
		}
		if move.Phase == "done" {
			if journal.DataReplaced && (move.Name == "server.properties" || move.Name == "eula.txt") {
				continue
			}
			digest, exists, e := treeDigest(ctx, server.root, move.Name)
			if e != nil {
				return e
			}
			if exists != (move.NewDigest != "") || digest != move.NewDigest {
				return domain.E(domain.CodeConflict, "restored data changed; manual repair required")
			}
			continue
		}
		// Recheck both sides of an interrupted rename, against persisted content
		// digests. No rename overwrites an existing destination.
		current, exists, e := treeDigest(ctx, server.root, move.Name)
		if e != nil {
			return e
		}
		old, hasOld, e := treeDigest(ctx, preimage.root, move.Name)
		if e != nil {
			return e
		}
		next, hasNext, e := treeDigest(ctx, stage.root, move.Name)
		if e != nil {
			return e
		}
		if hasOld && old != move.OldDigest || hasNext && next != move.NewDigest {
			return domain.E(domain.CodeConflict, "restore staging or preimage changed; manual repair required")
		}
		if move.Phase == "pending" || move.Phase == "moving_old" {
			if move.OldDigest != "" {
				if exists && current == move.OldDigest && !hasOld {
					move.Phase = "moving_old"
					if err = checkpoint(ctx, journal, "replacing"); err != nil {
						return err
					}
					if err = server.verify(); err != nil {
						return err
					}
					if err = preimage.verify(); err != nil {
						return err
					}
					if err = renameBetween(server.root, move.Name, preimage.root, move.Name); err != nil {
						return err
					}
					if err = errors.Join(syncDir(server.root, "."), syncDir(preimage.root, ".")); err != nil {
						return err
					}
					if err = s.failAt("restore_old_moved"); err != nil {
						return err
					}
					exists = false
					hasOld = true
				} else if exists || !hasOld {
					return domain.E(domain.CodeConflict, "restore old-data move is ambiguous; manual repair required")
				}
			} else if exists || hasOld {
				return domain.E(domain.CodeConflict, "unexpected data at restore target")
			}
			move.Phase = "old_moved"
			if err = checkpoint(ctx, journal, "replacing"); err != nil {
				return err
			}
		}
		if move.NewDigest != "" {
			if !exists && hasNext {
				move.Phase = "moving_new"
				if err = checkpoint(ctx, journal, "replacing"); err != nil {
					return err
				}
				if err = server.verify(); err != nil {
					return err
				}
				if err = stage.verify(); err != nil {
					return err
				}
				if err = renameBetween(stage.root, move.Name, server.root, move.Name); err != nil {
					return err
				}
				if err = errors.Join(syncDir(server.root, "."), syncDir(stage.root, ".")); err != nil {
					return err
				}
				if err = s.failAt("restore_new_moved"); err != nil {
					return err
				}
			} else if !exists || current != move.NewDigest || hasNext {
				return domain.E(domain.CodeConflict, "restore new-data move is ambiguous; manual repair required")
			}
		} else if exists || hasNext {
			return domain.E(domain.CodeConflict, "restore removal is ambiguous; manual repair required")
		}
		move.Phase = "done"
		if err = checkpoint(ctx, journal, "replacing"); err != nil {
			return err
		}
	}
	return nil
}

func verifyRestoreMarker(state *source, journal *restoreJournal) error {
	file, err := state.open("backup-restore.json", 1024)
	if err != nil {
		return err
	}
	defer file.f.Close()
	var marker restoreMarker
	decoder := json.NewDecoder(file.f)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || marker.OperationID != journal.OperationID || marker.BackupID != journal.BackupID {
		return domain.E(domain.CodeConflict, "restore marker ownership differs; manual repair required")
	}
	return state.verify()
}

// ReconcileRestores resumes only the already-confirmed requested restore under
// paused startup+stopped admission. Ambiguous data remains blocked for repair.
func (s *Service) ReconcileRestores(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id,journal_json,state FROM backup_restores WHERE state!='complete' ORDER BY updated_at,id`)
	if err != nil {
		return err
	}
	type entry struct{ id, raw, state string }
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.raw, &e.state); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var all error
	for _, e := range entries {
		var journal restoreJournal
		if json.Unmarshal([]byte(e.raw), &journal) != nil || journal.InstanceID != e.id || !validKey(journal.OperationID) || !validKey(journal.BackupID) || !validKey(journal.SafetyID) {
			all = errors.Join(all, errCorrupt)
			continue
		}
		err = s.instances.WithStoppedRecovery(ctx, e.id, func(session *instance.StoppedSession) error {
			operation, cancel := context.WithTimeout(ctx, 15*time.Minute)
			defer cancel()
			ctx := operation
			detail, err := session.Detail(ctx)
			if err != nil {
				return err
			}
			discovery, err := s.worlds.Discover(ctx, e.id)
			if err != nil {
				return err
			}
			m := journal.Manifest
			if m.InstanceID != e.id || m.Version != detail.Instance.MinecraftVersion || m.Flavor != detail.Instance.Flavor || m.Loader != detail.Instance.LoaderVersion || m.LevelName != detail.Config.LevelName || m.Layout != discovery.Layout {
				return domain.E(domain.CodeConflict, "restore recovery provenance changed; manual repair required")
			}
			dirs, err := session.Paths(ctx)
			if err != nil {
				return err
			}
			if err = s.restorePreconditions(ctx, dirs, e.id); err != nil {
				return err
			}
			state, err := openSource(dirs.State)
			if err != nil {
				return err
			}
			_, markerErr := state.root.Lstat("backup-restore.json")
			state.Close()
			if os.IsNotExist(markerErr) {
				if e.state == "committed" {
					return s.journalRestore(ctx, &journal, "complete")
				}
				if e.state != "prepared" {
					return domain.E(domain.CodeConflict, "restore blocker missing; manual repair required")
				}
				if err = s.writeRestoreMarker(ctx, dirs, journal); err != nil {
					return err
				}
			} else if markerErr != nil {
				return markerErr
			}
			return s.finishRestore(ctx, session, dirs, &journal)
		})
		if err != nil {
			all = errors.Join(all, fmt.Errorf("restore recovery %s: %w", e.id, err))
		}
	}
	if all != nil {
		return all
	}
	return s.reconcileArchives(ctx)
}
func (s *Service) reconcileArchives(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,instance_id,state,metadata_json FROM backups WHERE state IN ('staging','deleting')`)
	if err != nil {
		return err
	}
	type entry struct{ id, instance, state, raw string }
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.instance, &e.state, &e.raw); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !validKey(e.id) {
			return errCorrupt
		}
		if e.state == "deleting" {
			if err = s.deleteRetained(ctx, e.instance, e.id); err != nil {
				return err
			}
			continue
		}
		var b domain.Backup
		if json.Unmarshal([]byte(e.raw), &b) != nil {
			return errCorrupt
		}
		b.State = domain.BackupFailed
		raw, _ := json.Marshal(b)
		if _, err = s.db.ExecContext(ctx, `UPDATE backups SET state='failed',metadata_json=?,recovery='Manager interrupted capture or publication; archive is not usable.' WHERE id=?`, string(raw), e.id); err != nil {
			return err
		}
		dirs, err := s.paths.For(e.instance)
		if err != nil {
			return err
		}
		root, err := openSource(dirs.Backups)
		if err != nil {
			return err
		}
		if err = root.verify(); err == nil {
			err = errors.Join(root.root.RemoveAll(".stage-"+e.id), removeIfPresent(root.root, e.id+".pending"), removeIfPresent(root.root, e.id+".zip"))
		}
		root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func removeIfPresent(root *os.Root, name string) error {
	err := root.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
