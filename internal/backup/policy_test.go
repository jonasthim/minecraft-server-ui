package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestRetentionPolicyPersistenceCASAndAudit(t *testing.T) {
	s, _, _ := fixture(t, false)
	p, err := s.GetRetentionPolicy(t.Context(), "alpha")
	if err != nil || p != (RetentionPolicy{KeepLast: 10, KeepDays: 30}) {
		t.Fatal(p, err)
	}
	p, err = s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepLast: 0, KeepDays: 7}, testActor)
	if err != nil || p != (RetentionPolicy{KeepLast: 0, KeepDays: 7, Revision: 1}) {
		t.Fatal(p, err)
	}
	// A lost successful response is retryable without a second audit or revision.
	retry, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepLast: 0, KeepDays: 7}, testActor)
	if err != nil || retry != p {
		t.Fatal(retry, err)
	}
	_, err = s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepLast: 5, KeepDays: 7}, testActor)
	if domain.AsError(err).Code != domain.CodeConflict {
		t.Fatal("stale edit accepted", err)
	}
	other := New(Config{DB: s.db, Paths: s.paths, Instances: s.instances})
	got, err := other.GetRetentionPolicy(t.Context(), "alpha")
	if err != nil || got != p {
		t.Fatal(got, err)
	}
	var count int
	var raw, username string
	if err = s.db.QueryRow(`SELECT count(*),details_json,username FROM audit_log WHERE action='backups.retention_policy'`).Scan(&count, &raw, &username); err != nil {
		t.Fatal(err)
	}
	var audit struct{ Before, After RetentionPolicy }
	if err = json.Unmarshal([]byte(raw), &audit); err != nil || count != 1 || username != "admin" || audit.Before.KeepLast != 10 || audit.After != p {
		t.Fatal(count, raw, username, err)
	}
	if strings.Contains(raw, "password") || strings.Contains(raw, "credential") {
		t.Fatal("audit contains sensitive fields", raw)
	}
}

func TestRetentionPolicyValidationFreshAuthorityAndRollback(t *testing.T) {
	for _, input := range []RetentionPolicyInput{{KeepLast: -1}, {KeepLast: 10001}, {KeepDays: -1}, {KeepDays: 36501}, {ExpectedRevision: -1}} {
		s, _, _ := fixture(t, false)
		if _, err := s.SetRetentionPolicy(t.Context(), "alpha", input, testActor); domain.AsError(err).Code != domain.CodeValidationFailed {
			t.Fatal(input, err)
		}
	}
	s, _, _ := fixture(t, false)
	for _, id := range []string{"../alpha", "missing"} {
		if _, err := s.GetRetentionPolicy(t.Context(), id); err == nil {
			t.Fatal("invalid/missing instance accepted", id)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO users(id,username,role,created_at,updated_at)VALUES(2,'spare-admin','admin','now','now')`); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"role='operator'", "role='viewer'", "role='admin',disabled=1"} {
		if _, err := s.db.Exec(`UPDATE users SET ` + change + ` WHERE id=1`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{}, testActor); domain.AsError(err).Code != domain.CodeForbidden {
			t.Fatal("stale admin allowed", change, err)
		}
	}
	if _, err := s.db.Exec(`UPDATE users SET role='admin',disabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_log WHEN NEW.action='backups.retention_policy' BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepLast: 1, KeepDays: 1}, testActor); err == nil {
		t.Fatal("audit failure ignored")
	}
	p, err := s.GetRetentionPolicy(t.Context(), "alpha")
	if err != nil || p.Revision != 0 || p.KeepLast != 10 {
		t.Fatal("policy escaped rolled-back audit", p, err)
	}
}

// Real archive fixtures are sufficient for deletion tests; archive validation is
// independently exercised by capture/download tests.
func seedRetentionArchive(t *testing.T, s *Service, age time.Duration) string {
	t.Helper()
	id, err := key()
	if err != nil {
		t.Fatal(err)
	}
	b := domain.Backup{ID: id, InstanceID: "alpha", Trigger: domain.BackupAutomatic, Kind: domain.BackupWorld, State: domain.BackupReady, CreatedAt: time.Now().UTC().Add(-age)}
	raw, _ := json.Marshal(b)
	if _, err = s.db.Exec(`INSERT INTO backups(id,instance_id,job_id,state,metadata_json,created_at)VALUES(?,'alpha','fixture','ready',?,?)`, id, string(raw), b.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	dirs, _ := s.paths.For("alpha")
	if err = os.WriteFile(filepath.Join(dirs.Backups, id+".zip"), []byte("inert fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return id
}
func assertArchive(t *testing.T, s *Service, id string, want bool) {
	t.Helper()
	dirs, _ := s.paths.For("alpha")
	_, err := os.Stat(filepath.Join(dirs.Backups, id+".zip"))
	if (err == nil) != want {
		t.Fatalf("archive %s exists=%v want=%v (%v)", id, err == nil, want, err)
	}
}

func TestRetentionPolicySaveDefersDeletionAndAutomaticCaptureUsesLatest(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 48*time.Hour)
	if _, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepLast: 0, KeepDays: 1}, testActor); err != nil {
		t.Fatal(err)
	}
	assertArchive(t, s, id, true)
	makeBackup(t, s, domain.BackupWorld) // manual capture must not prune
	assertArchive(t, s, id, true)
	safety, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, domain.BackupSafety, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if done := await(t, s.queue, safety.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	assertArchive(t, s, id, true)
	job, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, domain.BackupAutomatic, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if done := await(t, s.queue, job.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	assertArchive(t, s, id, false)
}

func TestMalformedRetentionPolicyPreservesValidatedBackupAndArchive(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 1000*time.Hour)
	if _, err := s.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO backup_retention_policies(instance_id,keep_last,keep_days,revision,updated_at,updated_by)VALUES('alpha',-1,1,1,'now',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRetentionPolicy(t.Context(), "alpha"); err == nil {
		t.Fatal("corrupt policy accepted")
	}
	job, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, domain.BackupAutomatic, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.queue.Resume(); err != nil {
		t.Fatal(err)
	}
	done := await(t, s.queue, job.ID)
	if done.State != domain.JobFailed {
		t.Fatal("retention error hidden", done)
	}
	assertArchive(t, s, id, true)
	logs, err := s.queue.Logs(t.Context(), job.ID, 0, 100)
	if err != nil || len(logs) == 0 || !strings.Contains(logs[len(logs)-1].Message, "Backup validated and ready; automatic retention failed") {
		t.Fatal("job output hides valid backup", logs, err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM backups WHERE job_id=?`, job.ID).Scan(&state); err != nil || state != "ready" {
		t.Fatal("validated backup lost", state, err)
	}
	var outcome string
	if err = s.db.QueryRow(`SELECT details_json FROM audit_log WHERE action='backups.create_result' AND target=(SELECT id FROM backups WHERE job_id=?)`, job.ID).Scan(&outcome); err != nil || !strings.Contains(outcome, "validated_retention_failed") {
		t.Fatal("partial outcome missing", outcome, err)
	}
}

func TestRetentionPolicyLockCancellationAndLoosenBeforePrune(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 1000*time.Hour)
	// Occupy the shared policy admission gate before either operation takes a DB
	// connection, proving cancellation doesn't deadlock the one-connection pool.
	s.policyGate <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.SetRetentionPolicy(ctx, "alpha", RetentionPolicyInput{}, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := s.applyCurrentRetention(ctx, "alpha", time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-s.policyGate
	if _, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{}, testActor); err != nil {
		t.Fatal(err)
	}
	if err := s.applyCurrentRetention(t.Context(), "alpha", time.Now()); err != nil {
		t.Fatal(err)
	}
	assertArchive(t, s, id, true)
}

func TestRetentionPolicyMutationWaitsForAdmittedDeletion(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 1000*time.Hour)
	// Retention admits first and blocks at the existing download-reference mutex.
	s.mu.Lock()
	prune := make(chan error, 1)
	go func() { prune <- s.applyCurrentRetention(t.Context(), "alpha", time.Now()) }()
	deadline := time.After(2 * time.Second)
	for len(s.policyGate) == 0 {
		select {
		case <-deadline:
			s.mu.Unlock()
			t.Fatal("retention admission timeout")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	changed := make(chan error, 1)
	go func() {
		_, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{}, testActor)
		changed <- err
	}()
	select {
	case err := <-changed:
		s.mu.Unlock()
		t.Fatal("policy change escaped admitted retention", err)
	case <-time.After(20 * time.Millisecond):
	}
	assertArchive(t, s, id, true)
	s.mu.Unlock()
	if err := <-prune; err != nil {
		t.Fatal(err)
	}
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	assertArchive(t, s, id, false)
	p, err := s.GetRetentionPolicy(t.Context(), "alpha")
	if err != nil || p.KeepLast != 0 || p.KeepDays != 0 {
		t.Fatal(p, err)
	}
}

func TestRetentionReadsPolicyAfterPublication(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 1000*time.Hour)
	if _, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{KeepDays: 1}, testActor); err != nil {
		t.Fatal(err)
	}
	s.fault = func(phase string) error {
		if phase == "commit" {
			_, err := s.SetRetentionPolicy(t.Context(), "alpha", RetentionPolicyInput{ExpectedRevision: 1}, testActor)
			return err
		}
		return nil
	}
	job, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, domain.BackupAutomatic, testActor)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.queue.Resume(); err != nil {
		t.Fatal(err)
	}
	if done := await(t, s.queue, job.ID); done.State != domain.JobSucceeded {
		t.Fatal(done)
	}
	assertArchive(t, s, id, true)
}

func TestRetentionPreservesAllActiveRestoreReferences(t *testing.T) {
	for _, kind := range []string{"restore_target", "restore_safety", "queued", "running"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := fixture(t, false)
			id := seedRetentionArchive(t, s, 1000*time.Hour)
			var err error
			if kind == "restore_target" || kind == "restore_safety" {
				target, safety := id, "another"
				if kind == "restore_safety" {
					target, safety = "another", id
				}
				_, err = s.db.Exec(`INSERT INTO backup_restores(id,instance_id,backup_id,safety_id,job_id,state,journal_json,updated_at)VALUES('fixture','alpha',?,?,'fixture','interrupted','{}','now')`, target, safety)
			} else {
				_, err = s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload,requested_by,cancellable)VALUES('fixture','alpha','backup_restore',?,'now',?,1,1)`, kind, `{"backup_id":"`+id+`"}`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.applyCurrentRetention(t.Context(), "alpha", time.Now()); err != nil {
				t.Fatal(err)
			}
			assertArchive(t, s, id, true)
			if kind == "restore_target" || kind == "restore_safety" {
				_, err = s.db.Exec(`UPDATE backup_restores SET state='complete'`)
			} else {
				_, err = s.db.Exec(`UPDATE jobs SET state='failed' WHERE id='fixture'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.applyCurrentRetention(t.Context(), "alpha", time.Now()); err != nil {
				t.Fatal(err)
			}
			assertArchive(t, s, id, false)
		})
	}
}

func TestRetentionDeletionFailureRetainsRecoveryJournal(t *testing.T) {
	s, _, _ := fixture(t, false)
	id := seedRetentionArchive(t, s, 1000*time.Hour)
	dirs, _ := s.paths.For("alpha")
	archive := filepath.Join(dirs.Backups, id+".zip")
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(archive, 0700); err != nil {
		t.Fatal(err)
	}
	obstruction := filepath.Join(archive, "obstruction")
	if err := os.WriteFile(obstruction, []byte("inert"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.applyCurrentRetention(t.Context(), "alpha", time.Now()); err == nil {
		t.Fatal("deletion failure ignored")
	}
	var state, recovery string
	if err := s.db.QueryRow(`SELECT state,recovery FROM backups WHERE id=?`, id).Scan(&state, &recovery); err != nil || state != "deleting" || recovery == "" {
		t.Fatal(state, recovery, err)
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileArchives(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertArchive(t, s, id, false)
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM backups WHERE id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestPublishedBackupStageCleanupFailurePreservesArchiveAndReportsCause(t *testing.T) {
	for _, tc := range []struct {
		name             string
		trigger          domain.BackupTrigger
		corruptPolicy    bool
		outcome, message string
	}{
		{"manual", domain.BackupManual, false, "validated_cleanup_failed", "staging cleanup failed"},
		{"safety", domain.BackupSafety, false, "validated_cleanup_failed", "staging cleanup failed"},
		{"automatic", domain.BackupAutomatic, false, "validated_cleanup_failed", "staging cleanup failed"},
		{"retention_and_cleanup", domain.BackupAutomatic, true, "validated_retention_and_cleanup_failed", "automatic retention and staging cleanup failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := fixture(t, false)
			if tc.corruptPolicy {
				if _, err := s.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`INSERT INTO backup_retention_policies(instance_id,keep_last,keep_days,revision,updated_at,updated_by)VALUES('alpha',-1,1,1,'now',1)`); err != nil {
					t.Fatal(err)
				}
			}
			s.fault = func(phase string) error {
				if phase == "stage_cleanup" {
					return errors.New("injected staging cleanup failure with private-path-detail")
				}
				return nil
			}
			job, err := s.Enqueue(t.Context(), "alpha", domain.BackupWorld, tc.trigger, testActor)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.queue.Resume(); err != nil {
				t.Fatal(err)
			}
			done := await(t, s.queue, job.ID)
			if done.State != domain.JobFailed {
				t.Fatal("postpublication failure hidden", done)
			}
			records, err := s.List(t.Context(), "alpha")
			if err != nil || len(records) != 1 || records[0].State != domain.BackupReady {
				t.Fatal("published backup no longer ready", records, err)
			}
			download, err := s.Download(t.Context(), "alpha", records[0].ID, testActor)
			if err != nil {
				t.Fatal("validated archive unavailable", err)
			}
			if err = download.Close(); err != nil {
				t.Fatal(err)
			}
			dirs, _ := s.paths.For("alpha")
			if info, err := os.Stat(filepath.Join(dirs.Backups, ".stage-"+records[0].ID)); err != nil || !info.IsDir() {
				t.Fatal("failed staging cleanup fixture did not leave its directory", err)
			}
			var outcome struct {
				Outcome string `json:"outcome"`
			}
			var raw string
			if err = s.db.QueryRow(`SELECT details_json FROM audit_log WHERE action='backups.create_result' AND target=?`, records[0].ID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(raw), &outcome); err != nil || outcome.Outcome != tc.outcome {
				t.Fatal("cleanup misclassified", raw, err)
			}
			logs, err := s.queue.Logs(t.Context(), job.ID, 0, 100)
			if err != nil || len(logs) == 0 {
				t.Fatal(logs, err)
			}
			message := logs[len(logs)-1].Message
			if !strings.Contains(message, "Backup validated and ready; "+tc.message) || !strings.Contains(message, "validated archive remains available") || (!tc.corruptPolicy && strings.Contains(message, "retention")) || strings.Contains(message, "private-path-detail") || strings.Contains(message, "Local data may be partial") {
				t.Fatal("misleading or unsafe job message", message)
			}
		})
	}
}
