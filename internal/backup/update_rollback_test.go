package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestUpdateRollbackRequiresSeparateConsentAndNewCurrentSafetyBackup(t *testing.T) {
	for _, cut := range []string{"none", "restore_old_moved", "restore_new_moved"} {
		t.Run(cut, func(t *testing.T) { testUpdateRollbackRecovery(t, cut) })
	}
}
func testUpdateRollbackRecovery(t *testing.T, cut string) {
	s, native, data := fixture(t, false)
	original := makeBackup(t, s, domain.BackupFull)
	if err := s.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	dirs, _ := s.paths.For("alpha")
	worldFile := filepath.Join(dirs.Server, "world/level.dat")
	if err := os.WriteFile(worldFile, []byte("new-generation-world"), 0600); err != nil {
		t.Fatal(err)
	}
	// Synthetic newer selection remains current after an attempted boot. Its new
	// full safety archive must describe this tuple, never the older target tuple.
	if _, err := s.db.ExecContext(t.Context(), "UPDATE instances SET minecraft_version='1.18.2',java_major=17 WHERE id='alpha'"); err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("a", 32)
	transactionID := strings.Repeat("b", 32)
	generation := strings.Repeat("c", 32)
	parentJob := strings.Repeat("d", 32)
	history, _ := json.Marshal(map[string]any{"generation": generation, "plan": domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5"}}, "spec": domain.LaunchSpec{}})
	if _, err := s.db.ExecContext(t.Context(), "INSERT INTO lifecycle_generations(instance_id,generation,installation_json) VALUES('alpha',?,?)", generation, string(history)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,'alpha','preview',?,1,'update','boot_attempted',?,'{}','now')`, parent, parentJob, original.ID); err != nil {
		t.Fatal(err)
	}
	oldMarker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: parent, JobID: parentJob, Kind: "update"}
	if err := launcher.WriteUpdateBlocker(t.Context(), data, oldMarker); err != nil {
		t.Fatal(err)
	}
	if err := launcher.WriteWorldRiskBlocker(t.Context(), data, oldMarker); err != nil {
		t.Fatal(err)
	}

	queue := jobs.New(s.db, nil)
	s.queue = queue
	guard := testGuard{queue, native}
	s.instances = instance.New(s.db, s.paths, native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
	result := make(chan error, 1)
	if err := queue.Register("instance_rollback", func(ctx context.Context, e *jobs.Execution) (resultErr error) {
		defer func() { result <- resultErr }()
		return s.WithStoppedJob(ctx, e, func(c *StoppedCapture, session *instance.StoppedSession) error {
			request := UpdateRollbackRequest{ParentTransactionID: parent, TransactionID: transactionID, BackupID: original.ID, BackupSHA256: original.Checksum.Value, TargetGeneration: generation, Target: domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5"}, Confirmation: "alpha", AcknowledgeWorldRewind: false}
			if _, err := s.PrepareUpdateRollback(ctx, c, session, request); domain.AsError(err).Code != domain.CodeValidationFailed {
				return errors.New("missing world rewind consent accepted")
			}
			request.AcknowledgeWorldRewind = true
			prepared, err := s.PrepareUpdateRollback(ctx, c, session, request)
			if err != nil {
				return err
			}
			receipt, err := prepared.Receipt()
			if err != nil {
				return err
			}
			if receipt.SafetyID == original.ID {
				return errors.New("prior safety backup reused")
			}
			safety, err := s.record(ctx, "alpha", receipt.SafetyID)
			if err != nil || !safety.Protected || safety.Trigger != domain.BackupSafety {
				return errors.New("new safety archive not ready/protected")
			}
			var manifestRaw string
			if err = s.db.QueryRowContext(ctx, "SELECT manifest_json FROM backups WHERE id=?", receipt.SafetyID).Scan(&manifestRaw); err != nil {
				return err
			}
			var manifest Manifest
			if json.Unmarshal([]byte(manifestRaw), &manifest) != nil || manifest.Version != "1.18.2" {
				return errors.New("safety capture pretended older world selection")
			}
			if err = s.WithRollbackAdmission(ctx, prepared, func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "UPDATE update_transactions SET state='superseded',superseded_by=? WHERE id=?", transactionID, parent); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,parent_transaction_id,journal_json,updated_at) VALUES(?,'alpha','preview',?,1,'instance_rollback','prepared',?,?,'{}','now')`, transactionID, receipt.JobID, original.ID, parent)
				return err
			}); err != nil {
				return err
			}
			// A committed admission with the OLD marker must refuse world replacement.
			if err = session.WithCoordinatorScope(ctx, func(ctx context.Context, scope *instance.TransactionScope) error {
				return s.WithUpdateRollbackRecovery(ctx, scope, transactionID, func(*RollbackParticipant) error { return errors.New("old marker admitted") })
			}); err == nil {
				return errors.New("world restore admitted before marker handoff")
			}
			bytes, err := os.ReadFile(worldFile)
			if err != nil || string(bytes) != "new-generation-world" {
				return errors.New("admission rewound world before ownership handoff")
			}
			newMarker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: transactionID, JobID: receipt.JobID, Kind: "instance_rollback"}
			if err = launcher.TransferUpdateBlocker(ctx, data, oldMarker, newMarker); err != nil {
				return err
			}
			if cut != "none" {
				s.fault = func(at string) error {
					if at == cut {
						return errors.New("fixture interruption between durable moves")
					}
					return nil
				}
			}
			return finishRollbackFixture(ctx, s, session, transactionID)
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	job, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "instance_rollback", RequestedBy: 1, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-result:
		if (err != nil) != (cut != "none") {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("rollback job timed out")
	}
	if done := await(t, queue, job.ID); (done.State == domain.JobSucceeded) != (cut == "none") {
		t.Fatal(done)
	}
	if cut != "none" {
		if err = queue.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		s.fault = nil
		queue = jobs.New(s.db, nil)
		s.queue = queue
		guard := testGuard{queue, native}
		s.instances = instance.New(s.db, s.paths, native, instance.WithJobMutationGuard(guard), instance.WithRecoveryMutationGuard(guard))
		if err = queue.StartPaused(t.Context()); err != nil {
			t.Fatal(err)
		}
		defer queue.Stop(context.Background())
		if err = s.instances.WithStoppedRecovery(t.Context(), "alpha", func(session *instance.StoppedSession) error {
			return finishRollbackFixture(t.Context(), s, session, transactionID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	bytes, err := os.ReadFile(worldFile)
	if err != nil || string(bytes) != "original" {
		t.Fatal("explicit rollback did not restore world", err)
	}
	if _, err = os.Stat(filepath.Join(dirs.State, "update-world-risk.json")); err != nil {
		t.Fatal("backup participant cleared coordinator world-risk marker", err)
	}
}

func finishRollbackFixture(ctx context.Context, s *Service, session *instance.StoppedSession, transactionID string) error {
	return session.WithCoordinatorScope(ctx, func(ctx context.Context, scope *instance.TransactionScope) error {
		return s.WithUpdateRollbackRecovery(ctx, scope, transactionID, func(p *RollbackParticipant) error {
			copy := new(RollbackParticipant)
			reflect.ValueOf(copy).Elem().Set(reflect.ValueOf(p).Elem())
			if err := copy.Replace(ctx); domain.AsError(err).Code != domain.CodeForbidden {
				return errors.New("copied restore participant admitted")
			}
			if err := p.Replace(ctx); err != nil {
				return err
			}
			if err := scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error {
				if _, err := tx.ExecContext(ctx, "UPDATE instances SET minecraft_version='1.16.5',java_major=8 WHERE id='alpha'"); err != nil {
					return err
				}
				if err := p.Commit(ctx, tx); err != nil {
					return err
				}
				_, err := tx.ExecContext(ctx, "UPDATE update_transactions SET state='committed' WHERE id=?", transactionID)
				return err
			}); err != nil {
				return err
			}
			if err := scope.MaterializeRestoredProperties(ctx); err != nil {
				return err
			}
			return p.Finish(ctx)
		})
	})
}
