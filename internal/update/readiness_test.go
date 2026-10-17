package update

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestReadinessFindsOrphanMarkersWithoutActiveSQL(t *testing.T) {
	database := supersessionFixture(t)
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{config: Config{DB: database, Paths: paths}}
	if pending, err := service.HasPendingTransactions(t.Context()); err != nil || pending {
		t.Fatalf("valid terminal history pending=%v err=%v", pending, err)
	}
	dirs, err := paths.For("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dirs.State, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"update-transaction.json", "update-world-risk.json"} {
		if err = os.WriteFile(dirs.State+"/"+name, []byte("orphan"), 0600); err != nil {
			t.Fatal(err)
		}
		if pending, err := service.HasPendingTransactions(t.Context()); err != nil || !pending {
			t.Fatalf("orphan marker missing pending=%v err=%v", pending, err)
		}
		if err = service.CheckReady(t.Context(), "alpha"); err == nil {
			t.Fatal("orphan marker allowed ordinary admission")
		}
		if err = os.Remove(dirs.State + "/" + name); err != nil {
			t.Fatal(err)
		}
	}
}
func TestRollbackJobReadinessRequiresOriginalSeparateConsent(t *testing.T) {
	for _, acknowledge := range []bool{true, false} {
		t.Run(map[bool]string{true: "confirmed", false: "missing consent"}[acknowledge], func(t *testing.T) {
			database := supersessionFixture(t)
			paths, err := instance.NewPaths(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = database.ExecContext(t.Context(), "INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','now','now')"); err != nil {
				t.Fatal(err)
			}
			parent := strings.Repeat("2", 32)
			generation := strings.Repeat("3", 32)
			previewID := strings.Repeat("4", 32)
			parentJob := strings.Repeat("5", 32)
			if _, err = database.ExecContext(t.Context(), `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES(?,'alpha','preview',?,1,'update','boot_attempted',?,'{}','now')`, parent, parentJob, strings.Repeat("c", 32)); err != nil {
				t.Fatal(err)
			}
			if _, err = database.ExecContext(t.Context(), "INSERT INTO lifecycle_installations(instance_id,generation,installation_json) VALUES('alpha',?,'{}')", generation); err != nil {
				t.Fatal(err)
			}
			var revision string
			if err = database.QueryRowContext(t.Context(), "SELECT updated_at FROM instances WHERE id='alpha'").Scan(&revision); err != nil {
				t.Fatal(err)
			}
			preview := RollbackPreview{ID: previewID, InstanceID: "alpha", ParentTransactionID: parent, ActorID: 1, Snapshot: Snapshot{InstanceRevision: revision, Generation: generation}, BackupID: strings.Repeat("c", 32), BackupSHA256: strings.Repeat("f", 64), TargetGeneration: strings.Repeat("1", 32), ExpiresAt: time.Now().Add(time.Hour)}
			raw, _ := json.Marshal(preview)
			if _, err = database.ExecContext(t.Context(), "INSERT INTO update_previews(id,instance_id,actor_id,private_json,created_at,expires_at) VALUES(?,'alpha',1,?,'now',?)", previewID, string(raw), preview.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
				t.Fatal(err)
			}
			dirs, _ := paths.For("alpha")
			if err = os.MkdirAll(dirs.State, 0700); err != nil {
				t.Fatal(err)
			}
			marker := launcher.UpdateBlocker{Version: 1, InstanceID: "alpha", TransactionID: parent, JobID: parentJob, Kind: "update"}
			if err = launcher.WriteUpdateBlocker(t.Context(), pathsDataDir(paths, "alpha"), marker); err != nil {
				t.Fatal(err)
			}
			if err = launcher.WriteWorldRiskBlocker(t.Context(), pathsDataDir(paths, "alpha"), marker); err != nil {
				t.Fatal(err)
			}
			service := &Service{config: Config{DB: database, Paths: paths}}
			queue := jobs.New(database, nil)
			result := make(chan error, 1)
			if err = queue.Register("instance_rollback", func(ctx context.Context, e *jobs.Execution) error {
				e.Job.InstanceID = "foreign"
				e.Payload = []byte(`{"confirmation":"forged"}`)
				err := service.CheckJobReady(ctx, e, "alpha")
				result <- err
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err = queue.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer queue.Stop(context.Background())
			raw, _ = json.Marshal(rollbackJobInput{PreviewID: previewID, Confirmation: "alpha", AcknowledgeWorldRewind: acknowledge})
			if _, err = queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "instance_rollback", RequestedBy: 1, Payload: raw}); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-result:
				if acknowledge && err != nil {
					t.Fatal(err)
				}
				if !acknowledge && domain.AsError(err).Code != domain.CodeForbidden {
					t.Fatal("missing consent admitted", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no readiness result")
			}
		})
	}
}
