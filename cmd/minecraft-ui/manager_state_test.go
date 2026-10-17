package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
)

func TestManagerStateGrammarRejectsExtraOrMalformedArguments(t *testing.T) {
	for _, args := range [][]string{
		{}, {"snapshot", "extra"}, {"verify"}, {"verify", "--snapshot"},
		{"verify", "--snapshot", "../outside"}, {"verify", "--snapshot", strings.Repeat("A", 64)},
		{"migrate"}, {"snapshot", "--output", "/tmp/out"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--to-schema", "5", "--from-schema", "1"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "01", "--to-schema", "5"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "+1", "--to-schema", "5"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "0", "--to-schema", "5"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "1", "--to-schema", "9223372036854775808"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "1", "--from-schema", "2", "--to-schema", "5"},
		{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "1", "--to-schema", "5", "extra"},
		{"resume", "--snapshot", strings.Repeat("a", 64)},
		{"resume", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-review", "extra"},
		{"resume", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-review", "--confirm-manager-state-review"},
		{"restore"},
		{"restore", "--snapshot", strings.Repeat("a", 64)},
		{"restore", "--snapshot", strings.Repeat("A", 64), "--confirm-manager-state-restore"},
		{"restore", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-restore", "--resume-version", "v1.2.3"},
		{"restore", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-restore", "--resume-executable-sha256", strings.Repeat("a", 64)},
		{"restore", "--snapshot", strings.Repeat("a", 64), "--resume-version", "v1.2.3", "--confirm-manager-state-restore", "--resume-executable-sha256", strings.Repeat("a", 64)},
		{"restore", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-restore", "--resume-version", "v1.2.3", "--resume-executable-sha256", strings.Repeat("A", 64)},
		{"restore", "--snapshot", strings.Repeat("a", 64), "--confirm-manager-state-restore", "--resume-version", "v1.2.3", "--resume-executable-sha256", strings.Repeat("a", 64), "extra"},
	} {
		var output bytes.Buffer
		if err := runManagerState(context.Background(), args, &output); err == nil || !strings.HasPrefix(err.Error(), "usage:") || output.Len() != 0 {
			t.Fatalf("arguments %q were not rejected by grammar before configuration or wrote output: err=%v output=%s", args, err, output.String())
		}
	}
}

func TestDispatchedManagerStateRestoreRequiresExplicitImageForOlderSchema(t *testing.T) {
	dir := t.TempDir()
	snapshot := olderManagerStateCommandFixture(t, dir)
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	previousVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previousVersion })
	var output bytes.Buffer
	err := run(t.Context(), []string{"manager-state", "restore", "--snapshot", snapshot.ID, "--confirm-manager-state-restore"}, &output)
	if err == nil || !strings.Contains(err.Error(), "older schema requires an explicit resume image binding") || output.Len() != 0 {
		t.Fatalf("older-schema restore without an image binding did not fail safely: err=%v output=%q", err, output.String())
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("rejected restore created a maintenance hold: %v", err)
	}
}

func TestDispatchedManagerStateRestoreAcceptsExplicitFutureImageBinding(t *testing.T) {
	dir := t.TempDir()
	snapshot := olderManagerStateCommandFixture(t, dir)
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	previousVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previousVersion })
	resumeVersion, resumeHash := "v2.0.0", strings.Repeat("b", 64)
	var output bytes.Buffer
	err := run(t.Context(), []string{"manager-state", "restore", "--snapshot", snapshot.ID, "--confirm-manager-state-restore", "--resume-version", resumeVersion, "--resume-executable-sha256", resumeHash}, &output)
	if err != nil {
		t.Fatalf("older-schema restore with a complete explicit image binding failed: %v", err)
	}
	var restored managerstate.MaintenanceSummary
	if err := json.Unmarshal(output.Bytes(), &restored); err != nil || restored.State != "restored" || !restored.ReviewRequired || restored.AfterSchema != snapshot.Schema || restored.ExecutableSHA256 != resumeHash {
		t.Fatalf("explicitly bound older-schema restore returned an unexpected summary: %+v err=%v", restored, err)
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("startup was allowed for a restore bound to a different future image")
	}
}

func TestDispatchedManagerStateRestoreKeepsSafetySnapshotAndWorldData(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateCommandTestDirectory(t, dir)
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port,config_json) VALUES('restore-world','Before restore','vanilla','1.21.1',21,1024,25566,'{}')"); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	worldMarker := filepath.Join(dir, "instances", "restore-world", "server", "world", "marker.dat")
	if err := os.MkdirAll(filepath.Dir(worldMarker), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worldMarker, []byte("world bytes stay as-is"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	previousVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previousVersion })

	var output bytes.Buffer
	if err := run(t.Context(), []string{"manager-state", "snapshot"}, &output); err != nil {
		t.Fatal(err)
	}
	var captured managerstate.Summary
	if err := json.Unmarshal(output.Bytes(), &captured); err != nil || captured.Schema != db.LatestVersion() {
		t.Fatalf("latest-schema source snapshot missing: summary=%+v err=%v", captured, err)
	}
	mutator, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ExecContext(t.Context(), "UPDATE instances SET name='Changed after snapshot' WHERE id='restore-world'"); err != nil {
		_ = mutator.Close()
		t.Fatal(err)
	}
	if err := mutator.Close(); err != nil {
		t.Fatal(err)
	}

	output.Reset()
	if err := run(t.Context(), []string{"manager-state", "restore", "--snapshot", captured.ID, "--confirm-manager-state-restore"}, &output); err != nil {
		t.Fatalf("dispatched restore failed: %v", err)
	}
	var restored managerstate.MaintenanceSummary
	if err := json.Unmarshal(output.Bytes(), &restored); err != nil {
		t.Fatalf("restore did not return a typed summary: %v", err)
	}
	if restored.Operation != "restore" || restored.State != "restored" || restored.SnapshotID != captured.ID ||
		restored.SafetySnapshotID == "" || restored.BeforeSchema != db.LatestVersion() || restored.AfterSchema != db.LatestVersion() ||
		!restored.ReviewRequired || len(restored.ExecutableSHA256) != 64 || strings.Contains(output.String(), dir) {
		t.Fatalf("unexpected or unsafe restore summary: %+v output=%s", restored, output.String())
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("startup was allowed before explicit restore review")
	}
	assertInstanceName := func(databasePath, want string) {
		t.Helper()
		readDB, err := sql.Open("sqlite", db.FileURI(databasePath))
		if err != nil {
			t.Fatal(err)
		}
		defer readDB.Close()
		var got string
		if err := readDB.QueryRowContext(t.Context(), "SELECT name FROM instances WHERE id='restore-world'").Scan(&got); err != nil || got != want {
			t.Fatalf("database %s has name %q, want %q (err=%v)", databasePath, got, want, err)
		}
	}
	assertInstanceName(filepath.Join(dir, "manager.db"), "Before restore")
	assertInstanceName(filepath.Join(dir, "maintenance", restored.SafetySnapshotID, "manager.db"), "Changed after snapshot")
	worldBytes, err := os.ReadFile(worldMarker)
	if err != nil || string(worldBytes) != "world bytes stay as-is" {
		t.Fatalf("restore changed or removed world marker: %q err=%v", worldBytes, err)
	}

	output.Reset()
	if err := run(t.Context(), []string{"manager-state", "resume", "--snapshot", captured.ID, "--confirm-manager-state-review"}, &output); err != nil {
		t.Fatalf("explicit restore review/resume failed: %v", err)
	}
	var resumed managerstate.MaintenanceSummary
	if err := json.Unmarshal(output.Bytes(), &resumed); err != nil || resumed.State != "resumed" || resumed.ReviewRequired {
		t.Fatalf("restore review did not return resumed summary: %+v err=%v", resumed, err)
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("startup remained held after explicit restore review: %v", err)
	}
}

func TestManagerStateSchemaNumbersRequireCanonicalPositiveInt64(t *testing.T) {
	for _, value := range []string{"1", "5", "9223372036854775807"} {
		var parsed int64
		if !parseManagerSchema(value, &parsed) || strconv.FormatInt(parsed, 10) != value {
			t.Fatalf("rejected canonical schema %q: %d", value, parsed)
		}
	}
	for _, value := range []string{"", "0", "-1", "+1", "01", "1.0", " 1", "9223372036854775808"} {
		var parsed int64
		if parseManagerSchema(value, &parsed) {
			t.Fatalf("accepted noncanonical schema %q as %d", value, parsed)
		}
	}
}

func TestManagerStateCommandEmitsOnlyVerifiedSummary(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateCommandTestDirectory(t, dir)
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var output bytes.Buffer
	if err := run(t.Context(), []string{"manager-state", "snapshot"}, &output); err != nil {
		t.Fatal(err)
	}
	var summary managerstate.Summary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatal("command did not emit a JSON summary")
	}
	if summary.ID == "" || summary.Schema != db.LatestVersion() || strings.Contains(output.String(), dir) || strings.Contains(output.String(), "settings") {
		t.Fatalf("unsafe or incomplete summary: %s", output.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "maintenance", summary.ID, "snapshot.json")); err != nil {
		t.Fatal("verified summary lacks its published manifest")
	}
	output.Reset()
	if err := run(t.Context(), []string{"manager-state", "verify", "--snapshot", summary.ID}, &output); err != nil {
		t.Fatal(err)
	}
	var verified managerstate.Summary
	if err := json.Unmarshal(output.Bytes(), &verified); err != nil || verified != summary {
		t.Fatal("dispatched verification did not return the original summary")
	}
}

func olderManagerStateCommandFixture(t *testing.T, dir string) managerstate.Summary {
	t.Helper()
	prepareManagerStateCommandTestDirectory(t, dir)
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db")))
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		"CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY AUTOINCREMENT,version_id INTEGER NOT NULL,is_applied INTEGER NOT NULL,tstamp TIMESTAMP DEFAULT (datetime('now')))",
		"INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,1),(1,1)",
		"CREATE TABLE instances(id TEXT PRIMARY KEY,name TEXT NOT NULL,flavor TEXT NOT NULL,minecraft_version TEXT NOT NULL,java_major INTEGER NOT NULL,game_port INTEGER NOT NULL,config_json TEXT NOT NULL)",
		"CREATE TABLE process_identities(instance_id TEXT PRIMARY KEY,identity_json TEXT NOT NULL)",
		"CREATE TABLE artifacts(id TEXT PRIMARY KEY,instance_id TEXT,artifact_json TEXT NOT NULL)",
		"CREATE TABLE jobs(id TEXT PRIMARY KEY,instance_id TEXT,kind TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,error TEXT NOT NULL,created_at TEXT NOT NULL)",
		"INSERT INTO instances(id,name,flavor,minecraft_version,java_major,game_port,config_json) VALUES('old-world','Preserved server','vanilla','1.20.1',17,25565,'{}')",
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "manager.db"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := managerstate.Snapshot(t.Context(), dir, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Schema != 1 {
		t.Fatalf("fixture schema is %d, want 1", snapshot.Schema)
	}
	return snapshot
}

func TestDispatchedManagerStateMigrationRequiresExplicitResume(t *testing.T) {
	dir := t.TempDir()
	snapshot := olderManagerStateCommandFixture(t, dir)
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	previousVersion := version
	version = "v1.2.3"
	t.Cleanup(func() { version = previousVersion })

	var output bytes.Buffer
	args := []string{"manager-state", "migrate", "--snapshot", snapshot.ID, "--from-schema", "1", "--to-schema", strconv.FormatInt(db.LatestVersion(), 10)}
	if err := run(t.Context(), args, &output); err != nil {
		t.Fatalf("dispatched migration failed: %v", err)
	}
	var migrated managerstate.MaintenanceSummary
	if err := json.Unmarshal(output.Bytes(), &migrated); err != nil {
		t.Fatalf("migration did not return its typed JSON summary: %v", err)
	}
	if migrated.Format != 1 || migrated.Operation != "migrate" || migrated.State != "migrated" ||
		migrated.SnapshotID != snapshot.ID || migrated.BeforeSchema != 1 || migrated.AfterSchema != db.LatestVersion() ||
		!migrated.ReviewRequired || len(migrated.ExecutableSHA256) != 64 || strings.Contains(output.String(), dir) {
		t.Fatalf("unexpected migration summary: %+v output=%s", migrated, output.String())
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("startup was allowed before explicit resume")
	}
	if err := serve(t.Context(), config.Config{DataDir: dir, Address: "invalid-address", BaseURL: "http://127.0.0.1:8080"}); err == nil || !strings.Contains(err.Error(), "maintenance review is required") {
		t.Fatalf("serve did not stop at the maintenance hold: %v", err)
	}

	output.Reset()
	if err := run(t.Context(), []string{"manager-state", "resume", "--snapshot", snapshot.ID, "--confirm-manager-state-review"}, &output); err != nil {
		t.Fatalf("dispatched resume failed: %v", err)
	}
	var resumed managerstate.MaintenanceSummary
	if err := json.Unmarshal(output.Bytes(), &resumed); err != nil {
		t.Fatalf("resume did not return its typed JSON summary: %v", err)
	}
	if resumed.State != "resumed" || resumed.SnapshotID != snapshot.ID || resumed.ReviewRequired {
		t.Fatalf("unexpected resume summary: %+v", resumed)
	}
	if err := managerstate.CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("startup remained held after explicit resume: %v", err)
	}
}

func TestManagerStateMigrationErrorsRemainFixedAndPreserveCancellation(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateCommandTestDirectory(t, dir)
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var output bytes.Buffer
	args := []string{"migrate", "--snapshot", strings.Repeat("a", 64), "--from-schema", "1", "--to-schema", strconv.FormatInt(db.LatestVersion(), 10)}
	err := runManagerState(t.Context(), args, &output)
	if err == nil || err.Error() != "migration could not acquire safe manager state" || output.Len() != 0 || strings.Contains(err.Error(), dir) {
		t.Fatalf("migration failure was not a fixed safe category: err=%v output=%q", err, output.String())
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output.Reset()
	err = runManagerState(ctx, args, &output)
	if !errors.Is(err, context.Canceled) || output.Len() != 0 || strings.Contains(err.Error(), dir) {
		t.Fatalf("migration cancellation identity or safe error was lost: err=%v output=%q", err, output.String())
	}
}
