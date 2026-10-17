package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestUpdateParticipantUsesOneScopeAndPointerTransaction(t *testing.T) {
	s, actor, _ := fixture(t)
	if err := s.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	plan, err := s.providers[domain.FlavorVanilla].Resolve(t.Context(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.activate(t.Context(), "test", installation{Generation: strings.Repeat("a", 32), Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "immutable.jar", "nogui"}}}); err != nil {
		t.Fatal(err)
	}
	expected, err := s.UpdateSnapshot(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := lifecycleUpdatePaths(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dirs.Server, "world"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dirs.Server, "world", "sentinel"), []byte("world-is-current"), 0600); err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(s.db, nil)
	s.queue = queue
	result := make(chan error, 1)
	if err = queue.Register("instance_update", func(ctx context.Context, e *jobs.Execution) (resultErr error) {
		defer func() { result <- resultErr }()
		chosen := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.2"}
		plan, err := s.providers[domain.FlavorVanilla].Resolve(ctx, chosen)
		if err != nil {
			return err
		}
		prepared, err := s.PrepareUpdateForJob(ctx, e, expected, plan)
		if err != nil {
			return err
		}
		copy := *prepared
		if _, err = copy.Snapshot(); domain.AsError(err).Code != domain.CodeForbidden {
			return errors.New("copied staging capability accepted")
		}
		current, err := e.CheckCurrent(ctx)
		if err != nil {
			return err
		}
		transaction := strings.Repeat("b", 32)
		backupID := strings.Repeat("c", 32)
		metadata, _ := json.Marshal(domain.Backup{ID: backupID, InstanceID: "test", Kind: domain.BackupFull, Trigger: domain.BackupSafety, Protected: true, State: domain.BackupReady})
		if _, err = s.db.ExecContext(ctx, "INSERT INTO backups(id,instance_id,job_id,state,metadata_json,created_at) VALUES(?,'test',?,'ready',?,'now')", backupID, current.JobID, string(metadata)); err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,'test','preview',?,?,'update','prepared',?,'{}','now')`, transaction, current.JobID, actor.ID, backupID); err != nil {
			return err
		}
		if err = launcher.WriteUpdateBlocker(ctx, s.dataDir, launcher.UpdateBlocker{Version: 1, InstanceID: "test", TransactionID: transaction, JobID: current.JobID, Kind: "update"}); err != nil {
			return err
		}
		return s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
			return session.WithCoordinatorScope(ctx, func(ctx context.Context, scope *instance.TransactionScope) error {
				return s.WithUpdateParticipant(ctx, scope, prepared, transaction, func(p *UpdateParticipant) error {
					next, err := prepared.Snapshot()
					if err != nil {
						return err
					}
					return scope.WithSelectionParticipant(ctx, expected.Revision, chosen, next.Runtime, actor, func(selection *instance.SelectionParticipant) error {
						if err := p.Seal(ctx); err != nil {
							return err
						}
						return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error {
							if err := p.Commit(ctx, tx); err != nil {
								return err
							}
							if err := selection.Commit(ctx, tx); err != nil {
								return err
							}
							_, err := tx.ExecContext(ctx, "UPDATE update_transactions SET state='committed' WHERE id=?", transaction)
							return err
						})
					})
				})
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	job, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "instance_update", RequestedBy: actor.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, s, job); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	actual, err := s.UpdateSnapshot(t.Context(), "test")
	if err != nil || actual.Plan.Selection.MinecraftVersion != "1.20.2" || actual.Generation == expected.Generation {
		t.Fatalf("pointers not atomic: %+v %v", actual, err)
	}
	bytes, err := os.ReadFile(filepath.Join(dirs.Server, "world", "sentinel"))
	if err != nil || string(bytes) != "world-is-current" {
		t.Fatal("executable transaction changed world", err)
	}
	var intents int
	if err = s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM lifecycle_activations WHERE instance_id='test'").Scan(&intents); err != nil || intents != 0 {
		t.Fatal("coordinator minted ordinary activation journal", err)
	}
}
func lifecycleUpdatePaths(s *Service) (instance.Directories, error) {
	paths, err := instance.NewPaths(s.dataDir)
	if err != nil {
		return instance.Directories{}, err
	}
	return paths.For("test")
}
func TestUpdateCurrentJobExportsRejectForgeryBeforeNativeIO(t *testing.T) {
	s, actor, native := fixture(t)
	for _, invoke := range []func() error{
		func() error {
			_, err := s.PrepareUpdateForJob(t.Context(), &jobs.Execution{}, UpdateState{}, domain.InstallPlan{})
			return err
		},
		func() error { _, err := s.StopForUpdate(t.Context(), &jobs.Execution{}, actor, 0); return err },
		func() error {
			return s.StartForUpdate(t.Context(), &jobs.Execution{}, actor, func(context.Context, string, func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
				return domain.ProcessIdentity{}, nil
			})
		},
	} {
		if err := invoke(); domain.AsError(err).Code != domain.CodeForbidden {
			t.Fatal("forged execution accepted", err)
		}
	}
	if native.running {
		t.Fatal("forged execution started native process")
	}
}

func TestTargetProofRevalidatesProviderAndExpiresWithOriginalJob(t *testing.T) {
	s, actor, _ := fixture(t)
	if err := s.queue.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	selection := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1"}
	plan, _ := s.providers[domain.FlavorVanilla].Resolve(t.Context(), selection)
	if err := s.activate(t.Context(), "test", installation{Generation: strings.Repeat("e", 32), Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "immutable.jar", "nogui"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := s.UpdateSnapshot(t.Context(), "test")
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(s.db, nil)
	s.queue = queue
	var retained *PreparedUpdate
	result := make(chan error, 2)
	if err = queue.Register("instance_update_prepare", func(ctx context.Context, e *jobs.Execution) (out error) {
		defer func() { result <- out }()
		targetPlan, err := s.ResolveUpdate(ctx, "test", domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.2"})
		if err != nil {
			return err
		}
		retained, err = s.PrepareUpdateForJob(ctx, e, before, targetPlan)
		if err != nil {
			return err
		}
		if err = retained.WithTargetProof(ctx, nil, func(ctx context.Context, proof TargetProof) error {
			if proof.Selection.MinecraftVersion != "1.20.2" {
				return errors.New("proof used current tuple")
			}
			return nil
		}); err != nil {
			return err
		}
		copy := *retained
		if err = copy.WithTargetProof(ctx, nil, func(context.Context, TargetProof) error { return nil }); domain.AsError(err).Code != domain.CodeForbidden {
			return errors.New("copied target proof accepted")
		}
		s.providers[domain.FlavorVanilla] = testProvider{fail: true}
		err = retained.WithTargetProof(ctx, nil, func(context.Context, TargetProof) error { return errors.New("unverified target callback called") })
		s.providers[domain.FlavorVanilla] = testProvider{}
		if err == nil {
			return errors.New("changed provider evidence accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Register("instance_update", func(ctx context.Context, e *jobs.Execution) (out error) {
		defer func() { result <- out }()
		if err := retained.WithTargetProof(ctx, nil, func(context.Context, TargetProof) error { return nil }); domain.AsError(err).Code != domain.CodeForbidden {
			return errors.New("previous job proof accepted by new job")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	first, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "instance_update_prepare", RequestedBy: actor.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, s, first); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	second, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "instance_update", RequestedBy: actor.ID, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	if done := waitJob(t, s, second); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
}

func TestAutostartDefersVerifiedUpdateHoldBeforeGenerationMutation(t *testing.T) {
	s, _, native := fixture(t)
	selection := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1"}
	plan, _ := s.providers[domain.FlavorVanilla].Resolve(t.Context(), selection)
	generation := strings.Repeat("f", 32)
	if err := s.saveInstallation(t.Context(), "test", installation{Generation: generation, Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-jar", "immutable.jar", "nogui"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("INSERT INTO lifecycle_autostart(instance_id,desired,applied) VALUES('test',1,1)"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.deferAutostart = func(ctx context.Context, id string) (bool, error) { calls++; return true, nil }
	plans, err := s.AutostartPlans(t.Context())
	if err != nil || len(plans) != 0 || calls != 1 {
		t.Fatal(plans, calls, err)
	}
	current, _ := s.loadInstallation(t.Context(), "test")
	if current.Generation != generation || native.starts != 0 {
		t.Fatal("autostart rewrote blocked generation")
	}
	s.deferAutostart = func(context.Context, string) (bool, error) { return false, errors.New("foreign marker") }
	if _, err = s.AutostartPlans(t.Context()); err == nil {
		t.Fatal("foreign marker ignored")
	}
}

func TestDirectUpdateStartRequiresDurableIntentAndDoesNotMintGeneration(t *testing.T) {
	for _, intent := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing intent", true: "intent before native"}[intent], func(t *testing.T) {
			s, actor, native := fixture(t)
			if err := s.queue.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			plan, _ := s.providers[domain.FlavorVanilla].Resolve(t.Context(), domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1"})
			generation := strings.Repeat("9", 32)
			if err := s.activate(t.Context(), "test", installation{Generation: generation, Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-Xms512M", "-Xmx1024M", "-jar", "immutable.jar", "nogui"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE instance_settings SET eula_accepted=1 WHERE instance_id='test'"); err != nil {
				t.Fatal(err)
			}
			queue := jobs.New(s.db, nil)
			s.queue = queue
			result := make(chan error, 1)
			if err := queue.Register("instance_update", func(ctx context.Context, e *jobs.Execution) (out error) {
				defer func() { result <- out }()
				current, err := e.CheckCurrent(ctx)
				if err != nil {
					return err
				}
				if intent {
					raw, _ := json.Marshal(map[string]any{"version": 1, "boot_attempted": true, "after": map[string]any{"generation": generation}})
					if _, err = s.db.ExecContext(ctx, `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at)VALUES(?,'test','preview',?,?,'update','boot_attempted','backup',?,'now')`, strings.Repeat("8", 32), current.JobID, actor.ID, string(raw)); err != nil {
						return err
					}
				}
				err = s.StartForUpdateWithReceipt(ctx, e, actor, func(ctx context.Context, id string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
					var durable bool
					if queryErr := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM update_transactions WHERE state='boot_attempted' AND job_id=?)", current.JobID).Scan(&durable); queryErr != nil || !durable {
						return domain.ProcessIdentity{}, errors.New("native start preceded durable intent")
					}
					return start()
				}, func(ctx context.Context, id domain.ProcessIdentity) error {
					if id.InstanceID != "test" {
						return errors.New("start receipt differs")
					}
					return nil
				})
				if err == nil {
					return errors.New("fixture readiness absence reported success")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := queue.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer queue.Stop(context.Background())
			job, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "instance_update", RequestedBy: actor.ID, Payload: []byte(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			if err = <-result; err != nil {
				t.Fatal(err)
			}
			_ = waitJob(t, s, job)
			current, _ := s.loadInstallation(t.Context(), "test")
			if current.Generation != generation {
				t.Fatal("direct update start minted another generation")
			}
			expected := 0
			if intent {
				expected = 1
			}
			if native.starts != expected || native.running {
				t.Fatal("intent/readiness gate incorrect", native.starts, native.running)
			}
		})
	}
}
