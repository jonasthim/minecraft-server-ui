package managerstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"syscall"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"modernc.org/sqlite"
)

func olderMaintenanceFixture(t *testing.T) (string, Summary) {
	t.Helper()
	dir := t.TempDir()
	prepareManagerStateTestDirectory(t, dir)
	path := filepath.Join(dir, "manager.db")
	database, err := sql.Open("sqlite", db.FileURI(path))
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
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Snapshot(t.Context(), dir, "capture-version")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Schema != 1 {
		t.Fatalf("fixture schema = %d, want 1", snapshot.Schema)
	}
	return dir, snapshot
}

func testMaintenanceDeps() maintenanceDeps {
	return maintenanceDeps{
		imageHash: func(context.Context) (string, error) {
			return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
		},
		fence: func(_ context.Context, data, maintenance *os.Root) error {
			if err := syncDirectory(data); err != nil {
				return err
			}
			return syncDirectory(maintenance)
		},
	}
}

func TestMigratePreservesVerifiedSnapshotAndRequiresExplicitResume(t *testing.T) {
	dir, snapshot := olderMaintenanceFixture(t)
	rawBefore := snapshotFileHashes(t, dir, snapshot.ID)
	summary, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
		SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
	}, testMaintenanceDeps())
	if err != nil {
		t.Fatal(err)
	}
	if summary.State != "migrated" || summary.Operation != "migrate" || !summary.ReviewRequired ||
		summary.BeforeSchema != 1 || summary.AfterSchema != db.LatestVersion() {
		t.Fatalf("unexpected migration summary: %+v", summary)
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("startup was not held after migration")
	}
	database, err := sql.Open("sqlite", db.FileURI(filepath.Join(dir, "manager.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	version, err := db.CurrentVersion(t.Context(), database)
	if err != nil || version != db.LatestVersion() {
		_ = database.Close()
		t.Fatalf("database schema = %d, err=%v", version, err)
	}
	var name string
	if err := database.QueryRow("SELECT name FROM instances WHERE id='old-world'").Scan(&name); err != nil || name != "Preserved server" {
		_ = database.Close()
		t.Fatalf("existing record was not preserved: name=%q err=%v", name, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if got := snapshotFileHashes(t, dir, snapshot.ID); !jsonEqual(got, rawBefore) {
		t.Fatalf("raw recovery snapshot changed: before=%v after=%v", rawBefore, got)
	}
	resumed, err := resumeWithDeps(t.Context(), dir, "current-version", snapshot.ID, true, testMaintenanceDeps())
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != "resumed" || resumed.ReviewRequired {
		t.Fatalf("unexpected resume summary: %+v", resumed)
	}
	if err := CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("startup remained held after explicit resume: %v", err)
	}
}

func snapshotFileHashes(t *testing.T, dataDir, id string) map[string]string {
	t.Helper()
	result := map[string]string{}
	root := filepath.Join(dataDir, "maintenance", id)
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == manifestName {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		result[entry.Name()] = hex.EncodeToString(digest[:])
	}
	return result
}

func TestCheckStartupIsReadOnlyAndAllowsMissingDatabaseOrMaintenanceDirectory(t *testing.T) {
	t.Run("missing data root", func(t *testing.T) {
		parent := t.TempDir()
		path := filepath.Join(parent, "not-created")
		if err := CheckStartup(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CheckStartup created missing data path: %v", err)
		}
	})
	t.Run("existing data root without database", func(t *testing.T) {
		path := t.TempDir()
		before, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckStartup(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		after, err := os.ReadDir(path)
		if err != nil || len(after) != len(before) {
			t.Fatalf("CheckStartup changed empty data root: before=%v after=%v err=%v", before, after, err)
		}
	})
}

func TestCheckStartupBlocksForAnyHoldOrPendingState(t *testing.T) {
	for _, name := range []string{"hold.json", "hold.pending"} {
		t.Run(name, func(t *testing.T) {
			dir, _ := olderMaintenanceFixture(t)
			path := filepath.Join(dir, "maintenance", name)
			if err := os.WriteFile(path, []byte("malformed or unknown"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("startup accepted maintenance hold state")
			}
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "malformed or unknown" {
				t.Fatalf("startup check changed hold state: body=%q err=%v", body, err)
			}
		})
	}
}

func TestMigrateFailureAfterHoldKeepsStartupBlockedAndSnapshotRecoverable(t *testing.T) {
	tests := []struct {
		name string
		deps func() maintenanceDeps
	}{
		{
			name: "after hold publication",
			deps: func() maintenanceDeps {
				deps := testMaintenanceDeps()
				deps.afterHold = func() error { return errors.New("injected stop") }
				return deps
			},
		},
		{
			name: "during migration",
			deps: func() maintenanceDeps {
				deps := testMaintenanceDeps()
				deps.migrate = func(context.Context, *sql.DB) error { return errors.New("injected migration failure") }
				return deps
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, snapshot := olderMaintenanceFixture(t)
			before := snapshotFileHashes(t, dir, snapshot.ID)
			_, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
				SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
			}, test.deps())
			if err == nil {
				t.Fatal("injected failure was reported as success")
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("startup was not blocked after a post-hold failure")
			}
			if got := snapshotFileHashes(t, dir, snapshot.ID); !jsonEqual(got, before) {
				t.Fatalf("failure changed raw snapshot: before=%v after=%v", before, got)
			}
		})
	}
}

func TestMigrateRejectsConcurrentManagerLockWithoutPublishingHold(t *testing.T) {
	dir, snapshot := olderMaintenanceFixture(t)
	release, err := launcher.AcquireManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, migrateErr := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
		SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
	}, testMaintenanceDeps())
	release()
	if migrateErr == nil {
		t.Fatal("migration proceeded under another manager lock")
	}
	if err := CheckStartup(t.Context(), dir); err != nil {
		t.Fatalf("failed lock acquisition left a hold: %v", err)
	}
}

func TestMigrateRejectsInvalidOrStaleInputsBeforePublishingHold(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string, snapshot Summary) (string, MigrationOptions)
	}{
		{
			name: "missing snapshot",
			setup: func(_ *testing.T, _ string, snapshot Summary) (string, MigrationOptions) {
				return snapshot.ID + "-missing", MigrationOptions{SnapshotID: snapshot.ID + "-missing", FromSchema: 1, ToSchema: db.LatestVersion()}
			},
		},
		{
			name: "target mismatch",
			setup: func(_ *testing.T, _ string, snapshot Summary) (string, MigrationOptions) {
				return snapshot.ID, MigrationOptions{SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: 999}
			},
		},
		{
			name: "source schema mismatch",
			setup: func(_ *testing.T, _ string, snapshot Summary) (string, MigrationOptions) {
				return snapshot.ID, MigrationOptions{SnapshotID: snapshot.ID, FromSchema: 2, ToSchema: db.LatestVersion()}
			},
		},
		{
			name: "stale database",
			setup: func(t *testing.T, dir string, snapshot Summary) (string, MigrationOptions) {
				file, err := os.OpenFile(filepath.Join(dir, "manager.db"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("stale")); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return snapshot.ID, MigrationOptions{SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion()}
			},
		},
		{
			name: "sidecar difference",
			setup: func(t *testing.T, dir string, snapshot Summary) (string, MigrationOptions) {
				if err := os.WriteFile(filepath.Join(dir, "manager.db-wal"), []byte("foreign sidecar"), 0600); err != nil {
					t.Fatal(err)
				}
				return snapshot.ID, MigrationOptions{SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion()}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, snapshot := olderMaintenanceFixture(t)
			id, options := test.setup(t, dir, snapshot)
			options.SnapshotID = id
			if _, err := migrateWithDeps(t.Context(), dir, "current-version", options, testMaintenanceDeps()); err == nil {
				t.Fatal("migration accepted invalid or stale input")
			}
			if err := CheckStartup(t.Context(), dir); err != nil {
				t.Fatalf("preflight rejection published a hold: %v", err)
			}
		})
	}
}

func TestMigrateMissingDatabaseDoesNotCreateIt(t *testing.T) {
	dir := t.TempDir()
	prepareManagerStateTestDirectory(t, dir)
	if _, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
		SnapshotID: "missing-snapshot", FromSchema: 1, ToSchema: db.LatestVersion(),
	}, testMaintenanceDeps()); err == nil {
		t.Fatal("migration accepted a missing database")
	}
	if _, err := os.Lstat(filepath.Join(dir, "manager.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration created a missing database: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "maintenance")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed migration created maintenance storage: %v", err)
	}
}

func TestResumeRequiresMatchingImageAndExplicitConfirmation(t *testing.T) {
	dir, snapshot := olderMaintenanceFixture(t)
	_, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
		SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
	}, testMaintenanceDeps())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resumeWithDeps(t.Context(), dir, "current-version", snapshot.ID, false, testMaintenanceDeps()); err == nil {
		t.Fatal("resume proceeded without explicit confirmation")
	}
	changedImage := testMaintenanceDeps()
	changedImage.imageHash = func(context.Context) (string, error) {
		return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
	}
	if _, err := resumeWithDeps(t.Context(), dir, "current-version", snapshot.ID, true, changedImage); err == nil {
		t.Fatal("resume proceeded with a changed executable identity")
	}
	if err := CheckStartup(t.Context(), dir); err == nil {
		t.Fatal("rejected resume removed the startup hold")
	}
	if _, err := resumeWithDeps(t.Context(), dir, "current-version", snapshot.ID, true, testMaintenanceDeps()); err != nil {
		t.Fatalf("matching confirmed resume failed: %v", err)
	}
}

func TestMigratePreservesHoldOnCancellationAndDatabaseReplacement(t *testing.T) {
	t.Run("cancelled after hold", func(t *testing.T) {
		dir, snapshot := olderMaintenanceFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		deps := testMaintenanceDeps()
		deps.afterHold = func() error { cancel(); return nil }
		if _, err := migrateWithDeps(ctx, dir, "current-version", MigrationOptions{
			SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
		}, deps); !errors.Is(err, context.Canceled) {
			t.Fatalf("migration cancellation identity lost: %v", err)
		}
		if err := CheckStartup(t.Context(), dir); err == nil {
			t.Fatal("canceled migration did not leave startup blocked")
		}
	})
	t.Run("database path replaced after hold", func(t *testing.T) {
		dir, snapshot := olderMaintenanceFixture(t)
		replacement := []byte("foreign replacement")
		deps := testMaintenanceDeps()
		deps.afterHold = func() error {
			if err := os.Rename(filepath.Join(dir, "manager.db"), filepath.Join(dir, "manager.db.original")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "manager.db"), replacement, 0600)
		}
		if _, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
			SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
		}, deps); err == nil {
			t.Fatal("migration proceeded after database path replacement")
		}
		if got, err := os.ReadFile(filepath.Join(dir, "manager.db")); err != nil || string(got) != string(replacement) {
			t.Fatalf("foreign replacement was changed: content=%q err=%v", got, err)
		}
		if err := CheckStartup(t.Context(), dir); err == nil {
			t.Fatal("identity failure did not leave startup blocked")
		}
	})
}

func TestMigrateRejectsDataDirectorySubstitutionAtSQLiteConnectionBoundary(t *testing.T) {
	for _, boundary := range []string{"before ping", "after driver connect", "after ping"} {
		t.Run(boundary, func(t *testing.T) {
			dir, snapshot := olderMaintenanceFixture(t)
			parent := filepath.Dir(dir)
			moved := filepath.Join(parent, "original-data-root")
			var replacementPath string
			var replacementBefore []byte
			var holdAtRename []byte
			var databaseAtRename []byte
			var directoryAtRename os.FileInfo
			var hookAttempted bool
			var renameAttempted bool
			var renameErr error
			var migrationRan bool
			deps := testMaintenanceDeps()
			deps.migrate = func(context.Context, *sql.DB) error {
				migrationRan = true
				return errors.New("test migration callback must not run")
			}
			substitute := func() error {
				hookAttempted = true
				var err error
				holdAtRename, err = os.ReadFile(filepath.Join(dir, "maintenance", "hold.json"))
				if err != nil {
					return err
				}
				databaseAtRename, err = os.ReadFile(filepath.Join(dir, "manager.db"))
				if err != nil {
					return err
				}
				directoryAtRename, err = os.Stat(dir)
				if err != nil {
					return err
				}
				renameAttempted = true
				renameErr = os.Rename(dir, moved)
				if renameErr != nil {
					return renameErr
				}
				if err := os.Mkdir(dir, 0700); err != nil {
					return err
				}
				replacementPath = filepath.Join(dir, "manager.db")
				original, err := os.ReadFile(filepath.Join(moved, "manager.db"))
				if err != nil {
					return err
				}
				if err := os.WriteFile(replacementPath, original, 0600); err != nil {
					return err
				}
				replacementBefore, err = os.ReadFile(replacementPath)
				return err
			}
			switch boundary {
			case "before ping":
				deps.beforeDatabasePing = substitute
			case "after driver connect":
				deps.afterDriverConnect = substitute
			case "after ping":
				deps.afterDatabasePing = substitute
			}
			_, migrateErr := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
				SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
			}, deps)
			if migrateErr == nil {
				t.Fatal("migration accepted a replaced absolute data directory")
			}
			if migrationRan {
				t.Fatal("SQL migration callback ran after data-directory substitution")
			}
			if !hookAttempted {
				t.Fatal("connection-boundary substitution hook was not reached")
			}
			if !renameAttempted {
				t.Fatal("connection-boundary rename was not attempted")
			}
			if renameErr != nil {
				code, codeOK := boundedMaintenanceTestErrno(renameErr)
				if runtime.GOOS != "windows" || !codeOK || (code != 5 && code != 32) {
					nativeCode := int64(-1)
					if codeOK {
						nativeCode = int64(code)
					}
					t.Fatalf("directory substitution rename failed outside the expected Windows sharing/access denial: native_code=%d", nativeCode)
				}
				t.Logf("Windows data-directory rename denied: native_code=%d", code)
				var stageErr *maintenanceError
				if !errors.As(migrateErr, &stageErr) || stageErr.message != "manager database could not be safely opened for migration" {
					t.Fatal("Windows rename denial did not stop at the expected database-open stage")
				}
				if replacementPath != "" || replacementBefore != nil {
					t.Fatal("Windows rename refusal unexpectedly created a replacement")
				}
				currentDir, statErr := os.Stat(dir)
				if statErr != nil || !os.SameFile(directoryAtRename, currentDir) {
					t.Fatal("Windows rename refusal changed the original data directory")
				}
				if _, statErr := os.Stat(moved); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("Windows rename refusal created the moved data directory")
				}
				currentDatabase, readErr := os.ReadFile(filepath.Join(dir, "manager.db"))
				if readErr != nil || !bytes.Equal(currentDatabase, databaseAtRename) {
					t.Fatal("Windows rename refusal changed original database bytes")
				}
				currentHold, readErr := os.ReadFile(filepath.Join(dir, "maintenance", "hold.json"))
				if readErr != nil || !bytes.Equal(currentHold, holdAtRename) {
					t.Fatal("Windows rename refusal changed the published migration hold")
				}
				return
			}
			if len(replacementBefore) == 0 {
				t.Fatal("successful connection-boundary substitution produced no replacement database")
			}
			replacementAfter, err := os.ReadFile(replacementPath)
			if err != nil || !bytes.Equal(replacementAfter, replacementBefore) {
				t.Fatalf("SQLite modified the substituted database: err=%v", err)
			}
			if _, err := os.Stat(filepath.Join(moved, "maintenance", "hold.json")); err != nil {
				t.Fatalf("migration failure did not preserve its startup hold: %v", err)
			}
		})
	}
}

func boundedMaintenanceTestErrno(err error) (uint32, bool) {
	var code syscall.Errno
	if !errors.As(err, &code) || uint64(code) > 65535 {
		return 0, false
	}
	return uint32(code), true
}

func TestMigrationConnectorReappliesRequiredPragmasOnReplacementConnection(t *testing.T) {
	dir, _ := olderMaintenanceFixture(t)
	root, err := safepath.OpenRoot(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	expected, err := root.Lstat("manager.db")
	if err != nil {
		t.Fatal(err)
	}
	base, err := sqlite.NewConnector(db.FileURI(filepath.Join(dir, "manager.db")) + "?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	connector := migrationConnector{Connector: base, dataDir: dir, data: root, expected: expected}
	for connectionNumber := 1; connectionNumber <= 2; connectionNumber++ {
		connection, err := connector.Connect(t.Context())
		if err != nil {
			t.Fatalf("physical connection %d failed: %v", connectionNumber, err)
		}
		queryer, ok := connection.(driver.QueryerContext)
		if !ok {
			_ = connection.Close()
			t.Fatal("SQLite physical connection lacks query interface")
		}
		for _, want := range []struct {
			name  string
			value driver.Value
		}{
			{"journal_mode", "wal"},
			{"foreign_keys", int64(1)},
			{"synchronous", int64(2)},
			{"busy_timeout", int64(5000)},
		} {
			got, err := readMigrationPragma(t.Context(), queryer, want.name)
			if err != nil || !reflect.DeepEqual(got, want.value) {
				_ = connection.Close()
				t.Fatalf("connection %d pragma %s=%v, want %v, err=%v", connectionNumber, want.name, got, want.value, err)
			}
		}
		if err := connection.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResumeRejectsWrongSnapshotPhaseAndChangedSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string, snapshot Summary)
		id    func(Summary) string
	}{
		{
			name: "wrong snapshot id",
			id:   func(snapshot Summary) string { return snapshot.ID + "-wrong" },
		},
		{
			name: "migrating phase",
			setup: func(t *testing.T, dir string, _ Summary) {
				path := filepath.Join(dir, "maintenance", "hold.json")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var hold migrationHold
				if err := strictHoldJSON(body, &hold); err != nil {
					t.Fatal(err)
				}
				hold.Phase = "migrating"
				body, err = marshalHold(hold)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed referenced snapshot",
			setup: func(t *testing.T, dir string, snapshot Summary) {
				path := filepath.Join(dir, "maintenance", snapshot.ID, "manager.db")
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("changed snapshot")); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir, snapshot := olderMaintenanceFixture(t)
			if _, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
				SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
			}, testMaintenanceDeps()); err != nil {
				t.Fatal(err)
			}
			if test.setup != nil {
				test.setup(t, dir, snapshot)
			}
			id := snapshot.ID
			if test.id != nil {
				id = test.id(snapshot)
			}
			if _, err := resumeWithDeps(t.Context(), dir, "current-version", id, true, testMaintenanceDeps()); err == nil {
				t.Fatal("resume accepted a mismatched or changed maintenance state")
			}
			if err := CheckStartup(t.Context(), dir); err == nil {
				t.Fatal("rejected resume removed the startup hold")
			}
		})
	}
}

func TestMigrateRefusesForeignHoldAndPendingWithoutChangingThem(t *testing.T) {
	for _, name := range []string{"hold.json", "hold.pending"} {
		t.Run(name, func(t *testing.T) {
			dir, snapshot := olderMaintenanceFixture(t)
			path := filepath.Join(dir, "maintenance", name)
			foreign := []byte("foreign operation marker")
			if err := os.WriteFile(path, foreign, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := migrateWithDeps(t.Context(), dir, "current-version", MigrationOptions{
				SnapshotID: snapshot.ID, FromSchema: 1, ToSchema: db.LatestVersion(),
			}, testMaintenanceDeps()); err == nil {
				t.Fatal("migration overwrote foreign maintenance state")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != string(foreign) {
				t.Fatalf("foreign maintenance state changed: body=%q err=%v", got, err)
			}
		})
	}
}
