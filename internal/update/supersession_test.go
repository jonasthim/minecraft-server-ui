package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func supersessionFixture(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	for index, id := range []string{"alpha", "bravo"} {
		if _, err = database.ExecContext(t.Context(), "INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES(?,?,'vanilla','1.21.9',21,1024,?)", id, id, 25565+index); err != nil {
			t.Fatal(err)
		}
	}
	parent := strings.Repeat("a", 32)
	child := strings.Repeat("b", 32)
	targetID := strings.Repeat("c", 32)
	safetyID := strings.Repeat("d", 32)
	receipt := supersessionReceipt{ParentTransactionID: parent, TransactionID: child, InstanceID: "alpha", JobID: strings.Repeat("e", 32), ActorID: 1, BackupID: targetID, BackupSHA256: strings.Repeat("f", 64), SafetyID: safetyID, TargetGeneration: strings.Repeat("1", 32), ConsentedAt: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	target := domain.Backup{ID: targetID, InstanceID: "alpha", Kind: domain.BackupFull, State: domain.BackupReady, Checksum: domain.Checksum{Algorithm: "sha256", Value: receipt.BackupSHA256}}
	safety := domain.Backup{ID: safetyID, InstanceID: "alpha", Kind: domain.BackupFull, State: domain.BackupReady, Trigger: domain.BackupSafety, Protected: true}
	for _, record := range []domain.Backup{target, safety} {
		raw, _ := json.Marshal(record)
		if _, err = database.ExecContext(t.Context(), "INSERT INTO backups(id,instance_id,job_id,state,metadata_json,created_at) VALUES(?,'alpha','job','ready',?,'now')", record.ID, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,superseded_by,journal_json,updated_at) VALUES(?,'alpha','preview','job',1,'update','superseded',?,?,'{}','now')`, parent, targetID, child); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,parent_transaction_id,journal_json,updated_at) VALUES(?,'alpha','preview',?,1,'instance_rollback','complete',?,?,'{}','now')`, child, receipt.JobID, targetID, parent); err != nil {
		t.Fatal(err)
	}
	consent, _ := json.Marshal(map[string]any{"version": 1, "world_rewind": true, "receipt": receipt})
	if _, err = tx.ExecContext(t.Context(), `INSERT INTO backup_update_rollbacks(transaction_id,instance_id,job_id,actor_id,backup_id,safety_id,consent_json,journal_json,state,updated_at) VALUES(?,'alpha',?,1,?,?,?,'{}','complete','now')`, child, receipt.JobID, targetID, safetyID, string(consent)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return database
}
func TestSupersessionValidatesInsideOriginalReadOnlyTransaction(t *testing.T) {
	database := supersessionFixture(t)
	tx, err := database.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = ValidateSupersession(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
}
func TestSupersessionRefusesMalformedTerminalHandoffs(t *testing.T) {
	for _, query := range []string{
		"UPDATE backup_update_rollbacks SET consent_json=json_set(consent_json,'$.receipt.parent_transaction_id','foreign')",
		"UPDATE backup_update_rollbacks SET consent_json=json_set(consent_json,'$.world_rewind',json('false'))",
		"UPDATE backup_update_rollbacks SET consent_json='invalid'",
		"UPDATE backup_update_rollbacks SET state='prepared'",
		"UPDATE update_transactions SET instance_id='bravo' WHERE kind='instance_rollback'",
		"UPDATE backups SET metadata_json=json_set(metadata_json,'$.protected',json('false')) WHERE id='dddddddddddddddddddddddddddddddd'",
		"DELETE FROM backup_update_rollbacks",
	} {
		t.Run(query, func(t *testing.T) {
			database := supersessionFixture(t)
			if _, err := database.ExecContext(t.Context(), query); err != nil {
				t.Fatal(err)
			}
			if err := ValidateSupersession(context.Background(), database); err == nil {
				t.Fatal("invalid handoff accepted")
			}
		})
	}
}

func TestSupersessionRefusesCycleEvenWhenAllRowsTerminal(t *testing.T) {
	database := supersessionFixture(t)
	if _, err := database.ExecContext(t.Context(), "UPDATE update_transactions SET state='superseded',superseded_by=? WHERE kind='instance_rollback'", strings.Repeat("a", 32)); err != nil {
		t.Fatal(err)
	}
	var active int
	if err := database.QueryRowContext(t.Context(), "SELECT count(*) FROM update_transactions WHERE state NOT IN ('complete','rolled_back','superseded')").Scan(&active); err != nil || active != 0 {
		t.Fatalf("cycle fixture has active rows=%d err=%v", active, err)
	}
	if err := ValidateSupersession(t.Context(), database); err == nil {
		t.Fatal("terminal ownership cycle authorized drain")
	}
}
