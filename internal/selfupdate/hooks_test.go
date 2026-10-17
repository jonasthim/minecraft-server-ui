package selfupdate

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyProbesPreserveSpecialCharacterDatabaseAndRejectMissingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "schema # & % probe")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manager.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, createErr := database.Exec(`CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY,version_id INTEGER,is_applied INTEGER);
INSERT INTO goose_db_version VALUES(1,42,1);
CREATE TABLE users(id INTEGER PRIMARY KEY,role TEXT,disabled INTEGER);
INSERT INTO users VALUES(1,'admin',0),(2,'viewer',0),(3,'admin',1)`)
	closeErr := database.Close()
	if createErr != nil || closeErr != nil {
		t.Fatalf("create fixture: %v; close: %v", createErr, closeErr)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ReadOnlySchema(t.Context(), path); err != nil || got != 42 {
		t.Fatalf("existing schema=%d error=%v", got, err)
	}
	for _, tc := range []struct {
		id   int64
		want bool
	}{{1, true}, {2, false}, {3, false}, {4, false}} {
		if got, err := ReadOnlyActorAuthorized(t.Context(), path, tc.id); err != nil || got != tc.want {
			t.Fatalf("actor %d authorization=%v want=%v error=%v", tc.id, got, tc.want, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("read-only probes changed database: %v", err)
	}
	missing := filepath.Join(dir, "missing.db")
	if _, err := ReadOnlySchema(t.Context(), missing); err == nil {
		t.Fatal("schema probe accepted missing file")
	}
	if _, err := ReadOnlyActorAuthorized(t.Context(), missing, 1); err == nil {
		t.Fatal("actor probe accepted missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("probe created missing database: %v", err)
	}
}

func putChallenge(t *testing.T, dir string) Challenge {
	t.Helper()
	c := Challenge{Format: 1, Nonce: strings.Repeat("a", 64), FromVersion: "v1.2.2", ToVersion: "v1.2.3", ExpiresAt: time.Now().Add(time.Minute)}
	path := filepath.Join(dir, "selfupdate", "control")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(filepath.Join(path, "challenge.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestReadinessIsLoopbackAuthenticatedExactAndOneUse(t *testing.T) {
	dir := t.TempDir()
	c := putChallenge(t, dir)
	var ready atomic.Bool
	h := NewReadiness(dir, "v1.2.3", 16, ready.Load)
	call := func(remote, token string) int {
		r := httptest.NewRequest("GET", "/internal/update-ready", nil)
		r.RemoteAddr = remote
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code == 200 && !strings.Contains(w.Body.String(), `"version":"v1.2.3"`) {
			t.Fatal(w.Body.String())
		}
		return w.Code
	}
	if got := call("127.0.0.1:1234", c.Nonce); got != 503 {
		t.Fatal(got)
	}
	ready.Store(true)
	if got := call("192.0.2.1:1234", c.Nonce); got != 403 {
		t.Fatal(got)
	}
	if got := call("127.0.0.1:1234", "bad"); got != 403 {
		t.Fatal(got)
	}
	if got := call("127.0.0.1:1234", c.Nonce); got != 200 {
		t.Fatal(got)
	}
	if got := call("127.0.0.1:1234", c.Nonce); got != 403 {
		t.Fatal(got)
	}
}
func TestDrainReceiptRequiresCleanDBAndClosedDBPublication(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if receipt, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err != nil || receipt != nil {
		t.Fatal(receipt, err)
	}
	putChallenge(t, dir)
	receipt, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2")
	if err != nil || receipt == nil {
		t.Fatal(receipt, err)
	}
	if _, err := database.Exec(`INSERT INTO instance_mutations(instance_id,kind,staged_name,ownership_token) VALUES('test','delete','trash','token')`); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err == nil {
		t.Fatal("unfinished mutation allowed")
	}
	if _, err := database.Exec(`DELETE FROM instance_mutations`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := PublishDrainReceipt(ctx, dir, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "selfupdate", "control", "drained.json")); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadOnlySchema(ctx, filepath.Join(dir, "manager.db")); err != nil || got != db.LatestVersion() {
		t.Fatal(got, err)
	}
	missing := filepath.Join(t.TempDir(), "absent.db")
	if _, err := ReadOnlySchema(ctx, missing); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("probe created database")
	}
}
func TestDrainRefusesInstanceUpdateAndWorldRiskTransactionsAndOrphanMarkers(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	putChallenge(t, dir)
	if _, err := database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('test','Test','vanilla','1.21.1',21,1024,25565)`); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"update"} {
		for _, state := range []string{"prepared", "publishing", "committed", "boot_attempted", "complete", "rolled_back", "blocked"} {
			if _, err := database.Exec(`INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES('tx','test','preview','job',1,?,?,'backup','{}','now')`, kind, state); err != nil {
				t.Fatal(err)
			}
			_, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2")
			terminal := state == "complete" || state == "rolled_back"
			if (err == nil) != terminal {
				t.Fatalf("update %s/%s drain error %v", kind, state, err)
			}
			if _, err := database.Exec(`DELETE FROM update_transactions`); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, state := range []string{"prepared", "replacing", "data_replaced", "committed", "complete", "blocked"} {
		if _, err := database.Exec(`INSERT INTO backup_update_rollbacks(transaction_id,instance_id,job_id,actor_id,backup_id,safety_id,consent_json,journal_json,state,updated_at) VALUES('tx','test','job',1,'backup','safety','{}','{}',?,'now')`, state); err != nil {
			t.Fatal(err)
		}
		_, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2")
		if (err == nil) != (state == "complete") {
			t.Fatalf("world rollback %s drain error %v", state, err)
		}
		if _, err := database.Exec(`DELETE FROM backup_update_rollbacks`); err != nil {
			t.Fatal(err)
		}
	}
	stateDir := filepath.Join(dir, "instances", "test", "state")
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"update-transaction.json", "update-world-risk.json"} {
		if err := os.WriteFile(filepath.Join(stateDir, marker), []byte("malformed orphan"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err == nil {
			t.Fatal("orphan/malformed update marker allowed drain", marker)
		}
		if err := os.Remove(filepath.Join(stateDir, marker)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err != nil {
		t.Fatal("clean state blocked", err)
	}
}

func TestDrainRequiresValidatedTerminalSupersession(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	putChallenge(t, dir)
	if _, err := database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('test','Test','vanilla','1.21.1',21,1024,25565)`); err != nil {
		t.Fatal(err)
	}
	parentID, childID, backupID, safetyID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	target := domain.Backup{ID: backupID, InstanceID: "test", Kind: domain.BackupFull, Trigger: domain.BackupSafety, State: domain.BackupReady, Protected: true, Checksum: domain.Checksum{Algorithm: "sha256", Value: strings.Repeat("f", 64)}}
	safety := target
	safety.ID = safetyID
	for _, record := range []domain.Backup{target, safety} {
		raw, _ := json.Marshal(record)
		if _, err := database.Exec(`INSERT INTO backups(id,instance_id,job_id,state,metadata_json,created_at) VALUES(?,'test','job','ready',?,'now')`, record.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	receipt := backup.RollbackReceipt{ParentTransactionID: parentID, TransactionID: childID, InstanceID: "test", JobID: "rollback-job", ActorID: 1, BackupID: backupID, BackupSHA256: target.Checksum.Value, SafetyID: safetyID, TargetGeneration: strings.Repeat("e", 32), ConsentedAt: time.Now().UTC()}
	raw, _ := json.Marshal(map[string]any{"version": 1, "world_rewind": true, "receipt": receipt})
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,superseded_by,parent_transaction_id,journal_json,updated_at) VALUES(?,'test','preview','update-job',1,'update','superseded',?,?,NULL,'{}','now'),(?,'test','preview','rollback-job',1,'instance_rollback','complete',?,NULL,?,'{}','now')`, parentID, backupID, childID, childID, backupID, parentID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO backup_update_rollbacks(transaction_id,instance_id,job_id,actor_id,backup_id,safety_id,consent_json,journal_json,state,updated_at) VALUES(?,'test','rollback-job',1,?,?,?,'{}','complete','now')`, childID, backupID, safetyID, string(raw)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err != nil {
		t.Fatal("valid completed supersession refused", err)
	}
	for _, state := range []string{"prepared", "publishing", "committed", "boot_attempted", "blocked"} {
		if _, err := database.Exec(`UPDATE update_transactions SET state=? WHERE id=?`, state, childID); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err == nil {
			t.Fatal("active rollback allowed drain", state)
		}
	}
	if _, err := database.Exec(`UPDATE update_transactions SET state='complete' WHERE id=?`, childID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE update_transactions SET state='complete',superseded_by=NULL WHERE id=?`, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err == nil {
		t.Fatal("orphan child accepted as terminal")
	}
	if _, err := database.Exec(`UPDATE update_transactions SET state='superseded',superseded_by=? WHERE id=?`, childID, parentID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE backup_update_rollbacks SET consent_json='{}' WHERE transaction_id=?`, childID); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDrainReceipt(ctx, database, dir, "v1.2.2"); err == nil {
		t.Fatal("terminal supersession with invalid consent accepted")
	}
}

func TestWindowsCandidateProbeCapabilityNeverOpensManagerDB(t *testing.T) {
	for _, mode := range []string{"valid", "schema", "version", "hash", "nonce"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			control := controlDirectory(dir)
			if err := os.MkdirAll(control, 0700); err != nil {
				t.Fatal(err)
			}
			hash, err := ExecutableSHA256(ctx)
			if err != nil {
				t.Fatal(err)
			}
			challenge := Challenge{Format: 1, Nonce: digest([]byte("nonce")), FromVersion: "v1.2.2", ToVersion: "v1.2.3", ExpiresAt: time.Now().Add(time.Minute), ActorID: 1}
			request := CandidateProbeRequest{Format: 1, Nonce: challenge.Nonce, Version: challenge.ToVersion, Schema: 99, SHA256: hash}
			switch mode {
			case "schema":
				request.Schema++
			case "version":
				request.Version = "v1.2.4"
			case "hash":
				request.SHA256 = digest([]byte("other"))
			case "nonce":
				request.Nonce = digest([]byte("other"))
			}
			for name, value := range map[string]any{"challenge.json": challenge, "capability.json": request} {
				b, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(control, name), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = WriteSchemaProbeResult(ctx, dir, "v1.2.3", 99)
			if (err == nil) != (mode == "valid") {
				t.Fatal("capability request binding", mode, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "manager.db")); !os.IsNotExist(err) {
				t.Fatal("capability probe touched manager DB", err)
			}
			if mode == "valid" {
				b, err := os.ReadFile(filepath.Join(control, "probe.json"))
				if err != nil {
					t.Fatal(err)
				}
				var report ProbeResult
				if strictJSON(b, &report) != nil || report.Schema != 99 || report.Version != "v1.2.3" || report.ExecutableSHA256 != hash || report.Nonce != challenge.Nonce {
					t.Fatal(report)
				}
			}
		})
	}
}
