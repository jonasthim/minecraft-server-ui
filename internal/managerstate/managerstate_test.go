package managerstate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

func currentFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prepareManagerStateTestDirectory(t, dir)
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), `INSERT INTO settings(id,doc,updated_at) VALUES(1,'snapshot-marker','2026-10-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func rawSnapshotHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"manager.db", "manager.db-wal", "manager.db-shm", "manager.db-journal"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		out[name] = hex.EncodeToString(digest[:])
	}
	return out
}

func TestSnapshotAndVerifyPreserveRawFiles(t *testing.T) {
	dir := currentFixture(t)
	if err := os.Mkdir(filepath.Join(dir, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	before := rawSnapshotHashes(t, dir)
	summary, err := Snapshot(t.Context(), dir, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Format != 1 || summary.Schema != db.LatestVersion() || summary.BinaryVersion != "test-version" || summary.ID == "" || summary.FileCount < 1 || summary.TotalBytes <= 0 || len(summary.ManifestSHA256) != 64 {
		t.Fatalf("unexpected verified snapshot summary: %+v", summary)
	}
	if got := rawSnapshotHashes(t, dir); !jsonEqual(got, before) {
		t.Fatalf("source database or sidecars changed: before=%v after=%v", before, got)
	}
	verified, err := Verify(t.Context(), dir, summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified != summary {
		t.Fatalf("verify summary differs: snapshot=%+v verify=%+v", summary, verified)
	}
	if got := rawSnapshotHashes(t, dir); !jsonEqual(got, before) {
		t.Fatalf("verify changed source database or sidecars: before=%v after=%v", before, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "maintenance", summary.ID, "logs")); !os.IsNotExist(err) {
		t.Fatalf("snapshot copied unrelated manager data: %v", err)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestSnapshotRefusesMissingDatabaseWithoutCreatingIt(t *testing.T) {
	dir := t.TempDir()
	if _, err := Snapshot(t.Context(), dir, "test-version"); err == nil {
		t.Fatal("snapshot accepted a missing manager database")
	}
	if _, err := os.Lstat(filepath.Join(dir, "manager.db")); !os.IsNotExist(err) {
		t.Fatalf("snapshot created a manager database: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "maintenance")); !os.IsNotExist(err) {
		t.Fatalf("failed preflight created maintenance storage: %v", err)
	}
}

func TestSnapshotPreservesCancellationIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Snapshot(ctx, t.TempDir(), "test-version"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity was lost: %v", err)
	}
}

func TestSnapshotCancellationAfterCaptureRemovesOwnedPartial(t *testing.T) {
	dir := currentFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	_, err := snapshot(ctx, dir, "test-version", func(*os.Root) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation identity was lost: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "maintenance"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled snapshot left its owned partial directory behind: entries=%v err=%v", entries, err)
	}
}

type cancelAtErrorCall struct {
	context.Context
	calls    int
	cancelAt int
}

func (c *cancelAtErrorCall) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestCopyCapturedRetainsPartialOwnershipOnBetweenFileCancellation(t *testing.T) {
	sourcePath, destinationPath := t.TempDir(), t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(sourcePath, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	source, err := safepath.OpenRoot(t.Context(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := safepath.OpenRoot(t.Context(), destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	files := make([]sourceFile, 0, 2)
	for _, name := range []string{"first", "second"} {
		info, err := source.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, sourceFile{name: name, info: info})
	}
	ctx := &cancelAtErrorCall{Context: context.Background(), cancelAt: 3}
	records, owned, err := copyCaptured(ctx, source, destination, files)
	defer closeOwnedFiles(owned)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("between-file cancellation identity was lost: %v", err)
	}
	if len(records) != 1 || records[0].Name != "first" || len(owned) != 1 {
		t.Fatalf("partial copied state was not returned for cleanup: records=%v owned=%v", records, owned)
	}
}

func TestSnapshotBetweenFileCancellationCleansPartialAndPreservesCause(t *testing.T) {
	dir := currentFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "manager.db-journal"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := snapshotWithFileHook(ctx, dir, "test-version", nil, func(name string) {
		if name == "manager.db" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("between-file cancellation identity was lost: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "maintenance"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled snapshot left captured files behind: entries=%v err=%v", entries, err)
	}
}

func TestValidationCopyCancellationPreservesPartialOwnershipAndCause(t *testing.T) {
	sourcePath, destinationPath := t.TempDir(), t.TempDir()
	body := []byte("validation database bytes")
	if err := os.WriteFile(filepath.Join(sourcePath, "manager.db"), body, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := safepath.OpenRoot(t.Context(), sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destination, err := safepath.OpenRoot(t.Context(), destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	digest := sha256.Sum256(body)
	ctx := &cancelAtErrorCall{Context: context.Background(), cancelAt: 2}
	owned, err := copySnapshotForValidation(ctx, source, destination, []fileRecord{{Name: "manager.db", Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}})
	defer closeOwnedFiles(owned)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("validation-copy cancellation identity was lost: %v", err)
	}
	if len(owned) != 1 || owned["manager.db"].file == nil {
		t.Fatalf("partial validation copy was not returned for cleanup: %v", owned)
	}
}

func TestValidateSQLiteDoesNotCreateMissingValidationDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "replaced")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manager.db")
	if _, err := validateSQLite(t.Context(), path); err == nil {
		t.Fatal("validation accepted a missing database")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("SQLite validation created a replacement database: %v", err)
	}
}

func TestSQLiteOpenRefusesReplacedValidationPathWithoutCreatingDatabase(t *testing.T) {
	dataPath := t.TempDir()
	prepareManagerStateTestDirectory(t, dataPath)
	data, err := safepath.OpenRoot(t.Context(), dataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	managerSID := testManagerStateSID(t)
	maintenance, _, err := createPrivateDirectory(data, "maintenance", managerSID)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	validation, validationInfo, err := createPrivateDirectory(maintenance, ".validation", managerSID)
	if err != nil {
		t.Fatal(err)
	}
	defer validation.Close()
	databaseFile, err := validation.OpenFile("manager.db", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	databaseInfo, err := databaseFile.Stat()
	if err != nil {
		_ = databaseFile.Close()
		t.Fatal(err)
	}
	owned := ownedFiles{"manager.db": {file: databaseFile, info: databaseInfo}}
	defer closeOwnedFiles(owned)
	validateBinding := func() error {
		return verifyValidationBinding(t.Context(), dataPath, data, maintenance, ".validation", validation, validationInfo, owned)
	}
	beforeOpen := func() error {
		// Windows denies moving a directory while its files are held open. The
		// hook models a path substitution between binding checks without that
		// file pin; the retained directory handle and file identity are still
		// checked by the post-open binding validation.
		if err := closeOwnedFiles(owned); err != nil {
			return err
		}
		if err := validation.Remove("manager.db"); err != nil {
			return err
		}
		if err := maintenance.Rename(".validation", "moved-validation"); err != nil {
			return err
		}
		return maintenance.Mkdir(".validation", 0700)
	}
	_, err = validateSQLiteBound(t.Context(), filepath.Join(dataPath, "maintenance", ".validation", "manager.db"), validateBinding, beforeOpen)
	if err == nil || !strings.Contains(err.Error(), "binding changed") {
		t.Fatal("SQLite validation accepted a replaced validation directory")
	}
	replacement, err := maintenance.OpenRoot(".validation")
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := replacement.Lstat("manager.db"); !os.IsNotExist(err) {
		t.Fatalf("SQLite created a database in the replacement directory: %v", err)
	}
}

func TestCleanupRetainsCreationHandleAndPreservesReplacementFile(t *testing.T) {
	parent, err := safepath.OpenRoot(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	child, dirInfo, err := createPrivateDirectory(parent, "owned", testManagerStateSID(t))
	if err != nil {
		t.Fatal(err)
	}
	original, err := writeSyncedExclusive(child, "known", []byte("original"), 0600)
	if err != nil {
		_ = child.Close()
		t.Fatal(err)
	}
	if err := child.Remove("known"); err != nil {
		_ = original.file.Close()
		_ = child.Close()
		t.Fatal(err)
	}
	replacement, err := writeSyncedExclusive(child, "known", []byte("replacement"), 0600)
	if err != nil {
		_ = original.file.Close()
		_ = child.Close()
		t.Fatal(err)
	}
	if err := removeOwnedTree(parent, "owned", dirInfo, child, ownedFiles{"known": original}); err == nil {
		_ = replacement.file.Close()
		t.Fatal("cleanup accepted a replacement file after unlinking its owned file")
	}
	current, err := os.ReadFile(filepath.Join(parent.Name(), "owned", "known"))
	if err != nil || string(current) != "replacement" {
		t.Fatalf("cleanup removed or changed the replacement file: %q err=%v", current, err)
	}
	_ = replacement.file.Close()
}

func TestSnapshotRejectsSymlinkDatabase(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows reparse coverage requires the hosted fixture")
	}
	dir := currentFixture(t)
	original := filepath.Join(dir, "manager.db.original")
	if err := os.Rename(filepath.Join(dir, "manager.db"), original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, filepath.Join(dir, "manager.db")); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	if _, err := Snapshot(t.Context(), dir, "test-version"); err == nil {
		t.Fatal("snapshot followed a symlinked manager database")
	}
}

func TestSnapshotRejectsSidecarSetChangeAndCleansOwnedSnapshot(t *testing.T) {
	dir := currentFixture(t)
	_, err := snapshot(t.Context(), dir, "test-version", func(data *os.Root) error {
		file, err := data.OpenFile("manager.db-wal", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write([]byte("appeared-after-capture"))
		return errors.Join(writeErr, file.Close())
	})
	if err == nil {
		t.Fatal("snapshot accepted a sidecar created after source-set capture")
	}
	entries, err := os.ReadDir(filepath.Join(dir, "maintenance"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed snapshot left its owned partial directory behind: entries=%v err=%v", entries, err)
	}
}

func TestSnapshotRejectsHardlinkedDatabase(t *testing.T) {
	dir := currentFixture(t)
	if err := os.Link(filepath.Join(dir, "manager.db"), filepath.Join(dir, "manager.db-wal")); err != nil {
		t.Skipf("hardlinks unavailable on this host: %v", err)
	}
	if _, err := Snapshot(t.Context(), dir, "test-version"); err == nil {
		t.Fatal("snapshot accepted a hardlinked database sidecar")
	}
}

func TestSnapshotRefusesManagerLockContention(t *testing.T) {
	dir := currentFixture(t)
	release, err := launcher.AcquireManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Snapshot(t.Context(), dir, "test-version"); err == nil {
		t.Fatal("snapshot proceeded while another manager held the OS lock")
	}
}

func TestVerifyRejectsTamperedManifestAndUnknownFiles(t *testing.T) {
	dir := currentFixture(t)
	summary, err := Snapshot(t.Context(), dir, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Join(dir, "maintenance", summary.ID)
	manifestPath := filepath.Join(snapshotDir, "snapshot.json")
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(manifest, &value); err != nil {
		t.Fatal(err)
	}
	value["unexpected"] = "ignored-field"
	manifest, _ = json.Marshal(value)
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), dir, summary.ID); err == nil {
		t.Fatal("verify accepted an unknown manifest field")
	}

	if err := os.WriteFile(manifestPath, []byte(`{"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapshotDir, "unexpected.bin"), []byte("extra"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), dir, summary.ID); err == nil {
		t.Fatal("verify accepted a missing manifest member or extra snapshot file")
	}
}

func TestVerifyRejectsDuplicateManifestKeys(t *testing.T) {
	dir := currentFixture(t)
	summary, err := Snapshot(t.Context(), dir, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "maintenance", summary.ID, "snapshot.json")
	if err := os.WriteFile(manifestPath, []byte(`{"format":1,"format":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), dir, summary.ID); err == nil {
		t.Fatal("verify accepted duplicate manifest keys")
	}
}

func TestCleanupRefusesUnknownFilesAndReplacedDirectories(t *testing.T) {
	parentPath := t.TempDir()
	parent, err := safepath.OpenRoot(t.Context(), parentPath)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	managerSID := testManagerStateSID(t)
	child, dirInfo, err := createPrivateDirectory(parent, "owned", managerSID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Close() }()
	file, err := child.OpenFile("known", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("known")); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal("test file could not be inspected")
	}
	owned := ownedFiles{"known": {file: file, info: fileInfo}}
	unknown, err := child.OpenFile("foreign", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = unknown.Close()
	if err := removeOwnedTree(parent, "owned", dirInfo, child, owned); err == nil {
		t.Fatal("cleanup removed a directory containing an unowned file")
	}
	if _, err := os.Stat(filepath.Join(parentPath, "owned", "foreign")); err != nil {
		t.Fatal("cleanup removed the unknown file")
	}

	current, err := parent.Lstat("owned")
	if err != nil {
		t.Fatal(err)
	}
	child, err = parent.OpenRoot("owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Remove("foreign"); err != nil {
		t.Fatal(err)
	}
	if err := closeOwnedFiles(owned); err != nil {
		t.Fatal("test file handle could not be closed before directory replacement")
	}
	if err := child.Remove("known"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Rename("owned", "moved"); err != nil {
		t.Fatal(err)
	}
	other, err := parent.OpenRoot("moved")
	if err != nil {
		t.Fatal(err)
	}
	replacement, replacementInfo, err := createPrivateDirectory(parent, "owned", managerSID)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := replacement.OpenFile("foreign", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_ = foreign.Close()
	if err := removeOwnedTree(parent, "owned", current, other, ownedFiles{}); err == nil {
		t.Fatal("cleanup removed a replacement directory")
	}
	if _, err := os.Stat(filepath.Join(parentPath, "owned", "foreign")); err != nil {
		t.Fatal("cleanup removed the replacement directory contents")
	}
	foreignFile, err := replacement.Open("foreign")
	if err != nil {
		t.Fatal(err)
	}
	foreignInfo, err := foreignFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	_ = removeOwnedTree(parent, "owned", replacementInfo, replacement, ownedFiles{"foreign": {file: foreignFile, info: foreignInfo}})
	if err := child.Close(); err != nil {
		t.Fatal("directory identity handle could not be closed")
	}
	moved, err := parent.OpenRoot("moved")
	if err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedTree(parent, "moved", current, moved, ownedFiles{}); err != nil {
		t.Fatalf("original moved directory cleanup failed: %v", err)
	}
}

func mustStat(t *testing.T, root *os.Root, path ...string) os.FileInfo {
	t.Helper()
	info, err := root.Lstat(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestVerifyRejectsSnapshotMemberTampering(t *testing.T) {
	dir := currentFixture(t)
	summary, err := Snapshot(t.Context(), dir, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	member := filepath.Join(dir, "maintenance", summary.ID, "manager.db")
	file, err := os.OpenFile(member, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("tampered")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(t.Context(), dir, summary.ID); err == nil {
		t.Fatal("verify accepted changed database bytes")
	}
}

func TestSnapshotAcceptsSupportedOlderSchemaWithoutMigration(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateTestDirectory(t, dir)
	path := filepath.Join(dir, "manager.db")
	database, err := sql.Open("sqlite", db.FileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE goose_db_version(id INTEGER PRIMARY KEY AUTOINCREMENT,version_id INTEGER NOT NULL,is_applied INTEGER NOT NULL,timestamp TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`INSERT INTO goose_db_version(version_id,is_applied) VALUES(0,1),(1,1)`,
		`CREATE TABLE instances(id TEXT PRIMARY KEY,name TEXT NOT NULL,flavor TEXT NOT NULL,minecraft_version TEXT NOT NULL,java_major INTEGER NOT NULL,game_port INTEGER NOT NULL,config_json TEXT NOT NULL)`,
		`CREATE TABLE process_identities(instance_id TEXT PRIMARY KEY,identity_json TEXT NOT NULL)`,
		`CREATE TABLE artifacts(id TEXT PRIMARY KEY,instance_id TEXT,artifact_json TEXT NOT NULL)`,
		`CREATE TABLE jobs(id TEXT PRIMARY KEY,instance_id TEXT,kind TEXT NOT NULL,state TEXT NOT NULL,phase TEXT NOT NULL,error TEXT NOT NULL,created_at TEXT NOT NULL)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := Snapshot(t.Context(), dir, "older-version")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Schema != 1 {
		t.Fatalf("snapshot changed or misreported older schema: %+v", summary)
	}
	database, err = sql.Open("sqlite", db.FileURI(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version int64
	if err := database.QueryRow(`SELECT version_id FROM goose_db_version WHERE is_applied=1 ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("snapshot migrated source schema: version=%d err=%v", version, err)
	}
}

func TestManagerStateWALChild(t *testing.T) {
	if os.Getenv("MANAGERSTATE_WAL_CHILD") != "1" {
		return
	}
	dir := os.Getenv("MANAGERSTATE_WAL_DIR")
	database, err := db.Open(context.Background(), filepath.Join(dir, "manager.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture database initialization failed")
		os.Exit(21)
	}
	if _, err := database.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		fmt.Fprintln(os.Stderr, "fixture WAL preparation failed")
		os.Exit(22)
	}
	if _, err := database.Exec(`INSERT INTO settings(id,doc,updated_at) VALUES(1,'retained-wal-marker','2026-10-17T00:00:00Z')`); err != nil {
		fmt.Fprintln(os.Stderr, "fixture WAL commit failed")
		os.Exit(23)
	}
	// Deliberately skip database.Close so SQLite cannot checkpoint/remove the
	// committed WAL before the parent snapshots it.
	os.Exit(0)
}

func TestSnapshotPreservesCommittedRetainedWAL(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateTestDirectory(t, dir)
	command := exec.Command(os.Args[0], "-test.run=^TestManagerStateWALChild$")
	command.Env = append(os.Environ(), "MANAGERSTATE_WAL_CHILD=1", "MANAGERSTATE_WAL_DIR="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("WAL fixture child failed: %v (%s)", err, output)
	}
	wal := filepath.Join(dir, "manager.db-wal")
	walInfo, err := os.Stat(wal)
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("child did not leave a nonempty committed WAL: info=%v err=%v", walInfo, err)
	}
	before := rawSnapshotHashes(t, dir)
	summary, err := Snapshot(t.Context(), dir, "wal-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if summary.FileCount != len(before) {
		t.Fatalf("snapshot did not capture the exact retained database/sidecar set: summary=%+v raw=%v", summary, before)
	}
	if got := rawSnapshotHashes(t, dir); !jsonEqual(got, before) {
		t.Fatalf("snapshot changed raw WAL source: before=%v after=%v", before, got)
	}
	snapshotDir := filepath.Join(dir, "maintenance", summary.ID)
	for name := range before {
		if name == "manager.db" {
			continue
		}
		if _, err := os.Stat(filepath.Join(snapshotDir, name)); err != nil {
			t.Fatalf("snapshot omitted retained sidecar %s: %v", name, err)
		}
	}
	if _, err := Verify(t.Context(), dir, summary.ID); err != nil {
		t.Fatal(err)
	}

	withoutWAL := t.TempDir()
	if err := copyFileForTest(filepath.Join(snapshotDir, "manager.db"), filepath.Join(withoutWAL, "manager.db")); err != nil {
		t.Fatal(err)
	}
	without, err := sql.Open("sqlite", db.FileURI(filepath.Join(withoutWAL, "manager.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	var absent string
	noWALerr := without.QueryRow(`SELECT doc FROM settings WHERE id=1`).Scan(&absent)
	_ = without.Close()
	if noWALerr == nil || absent == "retained-wal-marker" {
		t.Fatalf("negative control unexpectedly retained committed WAL-only marker: value=%q err=%v", absent, noWALerr)
	}

	withWAL := t.TempDir()
	for name := range before {
		if err := copyFileForTest(filepath.Join(snapshotDir, name), filepath.Join(withWAL, name)); err != nil {
			t.Fatal(err)
		}
	}
	with, err := sql.Open("sqlite", db.FileURI(filepath.Join(withWAL, "manager.db"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	if err := with.QueryRow(`SELECT doc FROM settings WHERE id=1`).Scan(&marker); err != nil || marker != "retained-wal-marker" {
		_ = with.Close()
		t.Fatalf("copied WAL did not preserve committed marker: marker=%q err=%v", marker, err)
	}
	_ = with.Close()
}

func copyFileForTest(source, destination string) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(destination, body, 0600)
}

func TestVerifyRejectsInvalidSnapshotIDBeforeFilesystemAccess(t *testing.T) {
	for _, id := range []string{"", "../manager.db", strings.Repeat("a", 63), strings.Repeat("G", 64)} {
		if _, err := Verify(context.Background(), t.TempDir(), id); err == nil {
			t.Fatalf("accepted invalid snapshot id %q", id)
		}
	}
}
