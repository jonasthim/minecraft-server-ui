package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func maintenanceTestImage(t *testing.T, version string, schema int64, hash string) Installed {
	t.Helper()
	return Installed{Format: 1, Version: version, Schema: schema, SHA256: hash}
}

func putMaintenanceChallenge(t *testing.T, dataDir string, challenge MaintenanceChallenge) {
	t.Helper()
	control := controlDirectory(dataDir)
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(challenge)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "maintenance-challenge.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func validMaintenanceChallenge(t *testing.T, operation string, mode string) MaintenanceChallenge {
	t.Helper()
	ctx := context.Background()
	hash, err := ExecutableSHA256(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schema := db.LatestVersion()
	from := maintenanceTestImage(t, "v1.2.2", schema, hash)
	to := maintenanceTestImage(t, "v1.2.3", schema, strings.Repeat("b", 64))
	challenge := MaintenanceChallenge{
		Format:      1,
		OperationID: strings.Repeat("c", 64),
		Nonce:       strings.Repeat("a", 64),
		Mode:        mode,
		From:        &from,
		ExpiresAt:   time.Now().UTC().Add(time.Minute),
	}
	if mode == "apply" {
		challenge.To = &to
	}
	return challenge
}

func TestMaintenanceDrainUsesImageBindingWithoutWebActor(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dataDir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// An empty setup database has no web actor requirement for local maintenance.
	if _, err := database.Exec(`DELETE FROM users`); err != nil {
		t.Fatal(err)
	}
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	parsed, err := ReadMaintenanceChallenge(ctx, dataDir)
	if err != nil || parsed.OperationID != challenge.OperationID {
		t.Fatalf("read challenge=%+v err=%v", parsed, err)
	}
	receipt, err := PrepareMaintenanceDrainReceipt(ctx, database, dataDir, challenge.From.Version)
	if err != nil || receipt == nil {
		t.Fatalf("prepare receipt=%+v err=%v", receipt, err)
	}
	if receipt.Format != 1 || receipt.OperationID != challenge.OperationID || receipt.Nonce != challenge.Nonce || !receipt.Drained || receipt.Image != *challenge.From {
		t.Fatalf("receipt is not bound to current running image: %+v", receipt)
	}
}

func TestMissingMaintenanceChallengeSkipsDatabaseIO(t *testing.T) {
	receipt, err := PrepareMaintenanceDrainReceipt(context.Background(), nil, t.TempDir(), "v1.2.2")
	if err != nil || receipt != nil {
		t.Fatalf("missing challenge should be no-op without DB: receipt=%+v err=%v", receipt, err)
	}
}

func TestMaintenanceAndWebChallengesAreMutuallyExclusiveEvenWhenMalformed(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	control := controlDirectory(dataDir)
	if err := os.MkdirAll(control, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "challenge.json"), []byte("malformed ordinary"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMaintenanceChallenge(ctx, dataDir); err == nil {
		t.Fatal("maintenance challenge accepted alongside malformed web challenge")
	}
	if _, err := ReadChallenge(ctx, dataDir); err == nil {
		t.Fatal("web challenge accepted malformed collision")
	}
	if err := os.Remove(filepath.Join(control, "challenge.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(control, "maintenance-challenge.json"), []byte("malformed maintenance"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadChallenge(ctx, dataDir); err == nil {
		t.Fatal("web update challenge accepted alongside malformed maintenance challenge")
	}
}

func TestPrepareChallengesNoOpWhenOppositeFlowIsAbsent(t *testing.T) {
	ctx := context.Background()
	ordinaryOnly := t.TempDir()
	if err := os.MkdirAll(controlDirectory(ordinaryOnly), 0700); err != nil {
		t.Fatal(err)
	}
	ordinary := Challenge{Format: 1, Nonce: strings.Repeat("a", 64), FromVersion: "v1.2.2", ToVersion: "v1.2.3", ExpiresAt: time.Now().UTC().Add(time.Minute), ActorID: 1}
	ordinaryBytes, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDirectory(ordinaryOnly), "challenge.json"), ordinaryBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := PrepareMaintenanceDrainReceipt(ctx, nil, ordinaryOnly, "v1.2.2"); err != nil || receipt != nil {
		t.Fatalf("absent maintenance flow did not no-op: receipt=%+v err=%v", receipt, err)
	}
	if _, err := PrepareDrainReceipt(ctx, nil, ordinaryOnly, "v1.2.2"); err == nil || strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("present ordinary flow was rejected because maintenance file was absent: %v", err)
	}

	maintenanceOnly := t.TempDir()
	maintenance := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, maintenanceOnly, maintenance)
	if receipt, err := PrepareDrainReceipt(ctx, nil, maintenanceOnly, "v1.2.2"); err != nil || receipt != nil {
		t.Fatalf("absent ordinary flow did not no-op: receipt=%+v err=%v", receipt, err)
	}
	if _, err := PrepareMaintenanceDrainReceipt(ctx, nil, maintenanceOnly, "v1.2.2"); err == nil || strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("present maintenance flow was rejected because ordinary file was absent: %v", err)
	}
}

func TestPrepareFlowsRejectSimultaneousChallengeFiles(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	ordinary := Challenge{Format: 1, Nonce: strings.Repeat("a", 64), FromVersion: "v1.2.2", ToVersion: "v1.2.3", ExpiresAt: time.Now().UTC().Add(time.Minute), ActorID: 1}
	body, err := json.Marshal(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDirectory(dataDir), "challenge.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareDrainReceipt(ctx, nil, dataDir, "v1.2.2"); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("ordinary Prepare did not reject simultaneous challenge: %v", err)
	}
	if _, err := PrepareMaintenanceDrainReceipt(ctx, nil, dataDir, "v1.2.2"); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("maintenance Prepare did not reject simultaneous challenge: %v", err)
	}
}

func TestEveryExistingDrainBlockerBlocksBothPrepareFlows(t *testing.T) {
	type blocker struct {
		name          string
		needsInstance bool
		insertSQL     string
		marker        string
	}
	blockers := []blocker{
		{name: "running_job", insertSQL: `INSERT INTO jobs(id,kind,state,created_at) VALUES('blocker','test','running','now')`},
		{name: "armed_save_lease", needsInstance: true, insertSQL: `INSERT INTO backup_leases(id,instance_id,job_id,identity_json,expires_at,state,updated_at) VALUES('lease','drain','job','{}','later','armed','now')`},
		{name: "unfinished_backup_restore", needsInstance: true, insertSQL: `INSERT INTO backup_restores(id,instance_id,backup_id,safety_id,job_id,state,journal_json,updated_at) VALUES('restore','drain','backup','safety','job','replacing','{}','now')`},
		{name: "content_journal", needsInstance: true, insertSQL: `INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES('drain','preview','{}','applying')`},
		{name: "modpack_journal", needsInstance: true, insertSQL: `INSERT INTO modpack_journals(instance_id,preview_id,private_json,state) VALUES('drain','preview','{}','applying')`},
		{name: "lifecycle_activation", needsInstance: true, insertSQL: `INSERT INTO lifecycle_activations(instance_id,generation,installation_json) VALUES('drain','generation','{}')`},
		{name: "instance_mutation", needsInstance: true, insertSQL: `INSERT INTO instance_mutations(instance_id,kind,staged_name,ownership_token) VALUES('drain','delete','staged','token')`},
		{name: "update_transaction", needsInstance: true, insertSQL: `INSERT INTO update_transactions(id,instance_id,preview_id,job_id,actor_id,kind,state,backup_id,journal_json,updated_at) VALUES('tx','drain','preview','job',1,'update','prepared','backup','{}','now')`},
		{name: "backup_world_rollback", needsInstance: true, insertSQL: `INSERT INTO backup_update_rollbacks(transaction_id,instance_id,job_id,actor_id,backup_id,safety_id,consent_json,journal_json,state,updated_at) VALUES('tx','drain','job',1,'backup','safety','{}','{}','prepared','now')`},
		{name: "config_pending_marker", needsInstance: true, marker: "state/config.pending.json"},
		{name: "backup_restore_marker", needsInstance: true, marker: "state/backup-restore.json"},
		{name: "content_transaction_marker", needsInstance: true, marker: "state/content-transaction.json"},
		{name: "modpack_transaction_marker", needsInstance: true, marker: "state/modpack-transaction.json"},
		{name: "update_transaction_marker", needsInstance: true, marker: "state/update-transaction.json"},
		{name: "update_world_risk_marker", needsInstance: true, marker: "state/update-world-risk.json"},
		{name: "activation_pending_marker", needsInstance: true, marker: "server/.manager/activation.pending.json"},
	}
	ctx := context.Background()
	runningHash, err := ExecutableSHA256(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range blockers {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			database, err := db.Open(ctx, filepath.Join(base, "manager.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			if tc.needsInstance {
				if _, err := database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('drain','Drain fixture','vanilla','1.21.9',21,1024,25565)`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.insertSQL != "" {
				if _, err := database.Exec(tc.insertSQL); err != nil {
					t.Fatalf("insert blocker: %v", err)
				}
			}
			ordinaryDir := filepath.Join(base, "ordinary")
			maintenanceDir := filepath.Join(base, "maintenance")
			putChallenge(t, ordinaryDir)
			from := maintenanceTestImage(t, "v1.2.2", db.LatestVersion(), runningHash)
			to := maintenanceTestImage(t, "v1.2.3", db.LatestVersion(), strings.Repeat("b", 64))
			maintenance := MaintenanceChallenge{Format: 1, OperationID: strings.Repeat("c", 64), Nonce: strings.Repeat("a", 64), Mode: "apply", From: &from, To: &to, ExpiresAt: time.Now().UTC().Add(time.Minute)}
			putMaintenanceChallenge(t, maintenanceDir, maintenance)
			if tc.marker != "" {
				for _, dir := range []string{ordinaryDir, maintenanceDir} {
					marker := filepath.Join(dir, "instances", "drain", filepath.FromSlash(tc.marker))
					if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(marker, []byte("blocker"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := PrepareDrainReceipt(ctx, database, ordinaryDir, "v1.2.2"); err == nil {
				t.Fatal("ordinary update drain accepted blocker")
			}
			if _, err := PrepareMaintenanceDrainReceipt(ctx, database, maintenanceDir, "v1.2.2"); err == nil {
				t.Fatal("local maintenance drain accepted blocker")
			}
		})
	}
}

func TestMaintenanceChallengeStrictlyValidatesFieldsAndModeBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*MaintenanceChallenge)
	}{
		{"missing_to", func(c *MaintenanceChallenge) { c.To = nil }},
		{"wrong_schema", func(c *MaintenanceChallenge) { c.To.Schema++ }},
		{"expired", func(c *MaintenanceChallenge) { c.ExpiresAt = time.Now().Add(-time.Second) }},
		{"too_far", func(c *MaintenanceChallenge) { c.ExpiresAt = time.Now().Add(11 * time.Minute) }},
		{"bad_operation_id", func(c *MaintenanceChallenge) { c.OperationID = strings.Repeat("A", 64) }},
		{"bad_nonce", func(c *MaintenanceChallenge) { c.Nonce = "bad" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			challenge := validMaintenanceChallenge(t, "operation", "apply")
			tc.change(&challenge)
			putMaintenanceChallenge(t, dataDir, challenge)
			if _, err := ReadMaintenanceChallenge(context.Background(), dataDir); err == nil {
				t.Fatal("invalid maintenance challenge accepted")
			}
		})
	}

	dataDir := t.TempDir()
	fresh := validMaintenanceChallenge(t, "operation", "apply")
	fresh.From = nil
	putMaintenanceChallenge(t, dataDir, fresh)
	if got, err := ReadMaintenanceChallenge(context.Background(), dataDir); err != nil || got.From != nil {
		t.Fatalf("fresh apply without prior image rejected by parser: %+v %v", got, err)
	}

	dataDir = t.TempDir()
	uninstall := validMaintenanceChallenge(t, "operation", "uninstall")
	putMaintenanceChallenge(t, dataDir, uninstall)
	if _, err := ReadMaintenanceChallenge(context.Background(), dataDir); err != nil {
		t.Fatalf("valid uninstall challenge rejected: %v", err)
	}
}

func TestMaintenanceDrainRejectsImageMismatchAndDoesNotFabricateFreshDrain(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dataDir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	challenge.From = nil
	putMaintenanceChallenge(t, dataDir, challenge)
	if receipt, err := PrepareMaintenanceDrainReceipt(ctx, database, dataDir, "v1.2.2"); err == nil || receipt != nil {
		t.Fatalf("running manager fabricated a drain without From: %+v %v", receipt, err)
	}

	challenge = validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	if _, err := PrepareMaintenanceDrainReceipt(ctx, database, dataDir, "v1.2.4"); err == nil {
		t.Fatal("wrong running image version accepted")
	}
	challenge.From.SHA256 = strings.Repeat("d", 64)
	putMaintenanceChallenge(t, dataDir, challenge)
	if _, err := PrepareMaintenanceDrainReceipt(ctx, database, dataDir, challenge.From.Version); err == nil {
		t.Fatal("wrong running executable hash accepted")
	}
}

func TestPublishMaintenanceDrainReceiptIsExclusiveAndBound(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	receipt := &MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	if err := PublishMaintenanceDrainReceipt(ctx, dataDir, receipt); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(controlDirectory(dataDir), "maintenance-drained.pending")); !os.IsNotExist(err) {
		t.Fatalf("pending name remains after successful publication: %v", err)
	}
	output := filepath.Join(controlDirectory(dataDir), "maintenance-drained.json")
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishMaintenanceDrainReceipt(ctx, dataDir, receipt); err == nil {
		t.Fatal("existing receipt was overwritten")
	}
	after, err := os.ReadFile(output)
	if err != nil || string(after) != string(before) {
		t.Fatalf("existing receipt changed: %v", err)
	}

	other := *receipt
	other.OperationID = strings.Repeat("e", 64)
	if err := PublishMaintenanceDrainReceipt(ctx, dataDir, &other); err == nil {
		t.Fatal("receipt for different operation accepted")
	}
}

func TestMaintenanceReceiptLinkCollisionPreservesForeignOutput(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	receipt := &MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	foreign := []byte("foreign output must survive")
	err = publishMaintenanceDrainReceipt(ctx, root, challenge, receipt, func() error {
		return os.WriteFile(filepath.Join(controlDirectory(dataDir), "maintenance-drained.json"), foreign, 0600)
	})
	if err == nil {
		t.Fatal("publication unexpectedly replaced a colliding output")
	}
	got, readErr := os.ReadFile(filepath.Join(controlDirectory(dataDir), "maintenance-drained.json"))
	if readErr != nil || string(got) != string(foreign) {
		t.Fatalf("foreign output changed: data=%q err=%v", got, readErr)
	}
	if _, err := root.Lstat("maintenance-drained.pending"); !os.IsNotExist(err) {
		t.Fatalf("owned pending name was not cleaned after failed link: %v", err)
	}
}

func TestMaintenanceReceiptRejectsAndPreservesExternalHardlink(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	root, err := safepath.OpenRoot(ctx, controlDirectory(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	receipt := &MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	external := filepath.Join(dataDir, "outside-control-hardlink")
	err = publishMaintenanceDrainReceipt(ctx, root, challenge, receipt, func() error {
		return os.Link(filepath.Join(controlDirectory(dataDir), "maintenance-drained.pending"), external)
	})
	if err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("external hard link was not refused: %v", err)
	}
	for _, name := range []string{"maintenance-drained.pending", "maintenance-drained.json"} {
		if _, err := root.Lstat(name); err != nil {
			t.Fatalf("uncertain publication did not preserve %s: %v", name, err)
		}
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external alias was not preserved: %v", err)
	}
}

func TestMaintenanceReceiptWriteCancellationClosesAndCleansOwnedPending(t *testing.T) {
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	root, err := safepath.OpenRoot(context.Background(), controlDirectory(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	receipt := &MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = publishMaintenanceDrainReceipt(ctx, root, challenge, receipt, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write cancellation cause lost: %v", err)
	}
	if _, err := root.Lstat("maintenance-drained.pending"); !os.IsNotExist(err) {
		t.Fatalf("cancelled write did not clean its owned pending file: %v", err)
	}
}

func TestMaintenanceReadinessIsLoopbackNonceBoundAndOneUse(t *testing.T) {
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	hash, err := ExecutableSHA256(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	challenge.To.SHA256 = hash
	putMaintenanceChallenge(t, dataDir, challenge)
	var ready atomic.Bool
	ready.Store(true)
	handler := NewMaintenanceReadiness(dataDir, challenge.To.Version, challenge.To.Schema, ready.Load)
	call := func(method, remote, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/internal/maintenance-ready", nil)
		req.RemoteAddr = remote
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := call(http.MethodPost, "127.0.0.1:1234", challenge.Nonce); response.Code != http.StatusForbidden {
		t.Fatalf("POST status=%d", response.Code)
	}
	if response := call(http.MethodGet, "192.0.2.1:1234", challenge.Nonce); response.Code != http.StatusForbidden {
		t.Fatalf("remote status=%d", response.Code)
	}
	if response := call(http.MethodGet, "127.0.0.1:1234", "wrong"); response.Code != http.StatusForbidden {
		t.Fatalf("wrong token status=%d", response.Code)
	}
	response := call(http.MethodGet, "127.0.0.1:1234", challenge.Nonce)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), challenge.OperationID) || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("readiness status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call(http.MethodGet, "127.0.0.1:1234", challenge.Nonce); response.Code != http.StatusForbidden {
		t.Fatalf("replay status=%d", response.Code)
	}
}

func TestMaintenanceReadinessRechecksChallengeAndDoesNotConsumeOnNotReady(t *testing.T) {
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	hash, err := ExecutableSHA256(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	challenge.To.SHA256 = hash
	putMaintenanceChallenge(t, dataDir, challenge)
	var ready atomic.Bool
	handler := NewMaintenanceReadiness(dataDir, challenge.To.Version, challenge.To.Schema, ready.Load)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/internal/maintenance-ready", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer "+challenge.Nonce)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	if response := call(); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status=%d", response.Code)
	}
	ready.Store(true)
	changed := challenge
	changed.OperationID = strings.Repeat("f", 64)
	putMaintenanceChallenge(t, dataDir, changed)
	if response := call(); response.Code != http.StatusForbidden {
		t.Fatalf("replaced challenge status=%d", response.Code)
	}
}

func TestPublishMaintenanceReceiptCancellationPreservesCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dataDir := t.TempDir()
	challenge := validMaintenanceChallenge(t, "operation", "apply")
	putMaintenanceChallenge(t, dataDir, challenge)
	receipt := &MaintenanceDrainReceipt{Format: 1, OperationID: challenge.OperationID, Nonce: challenge.Nonce, Image: *challenge.From, Drained: true, CreatedAt: time.Now().UTC()}
	err := PublishMaintenanceDrainReceipt(ctx, dataDir, receipt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation cause lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(controlDirectory(dataDir), "maintenance-drained.json")); !os.IsNotExist(err) {
		t.Fatalf("cancelled publication left output: %v", err)
	}
}
