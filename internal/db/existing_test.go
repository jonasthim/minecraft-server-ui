package db

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingUsesDurableCurrentDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager #&.db")
	created, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var journal string
	if err := database.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%q: %v", journal, err)
	}
	for pragma, want := range map[string]int{"synchronous": 2, "foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := database.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d, want %d: %v", pragma, got, want, err)
		}
	}
	if _, err := database.ExecContext(t.Context(), "CREATE TABLE recovery_write_probe (value TEXT)"); err != nil {
		t.Fatalf("existing connection is not writable: %v", err)
	}
}

func TestOpenExistingDoesNotCreateOrMigrate(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		parent := filepath.Join(t.TempDir(), "absent")
		if database, err := OpenExisting(t.Context(), filepath.Join(parent, "manager.db")); err == nil || database != nil {
			t.Fatal("missing database accepted")
		}
		if _, err := os.Stat(parent); !os.IsNotExist(err) {
			t.Fatalf("missing parent created: %v", err)
		}
	})
	for _, tc := range []struct {
		name    string
		version int64
	}{{"older", LatestVersion() - 1}, {"newer", LatestVersion() + 1}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "manager.db")
			database, err := sql.Open("sqlite", FileURI(path))
			if err != nil {
				t.Fatal(err)
			}
			_, err = database.ExecContext(t.Context(), "CREATE TABLE goose_db_version (id INTEGER PRIMARY KEY, version_id INTEGER, is_applied INTEGER)")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = database.ExecContext(t.Context(), "INSERT INTO goose_db_version VALUES (1,?,1)", tc.version); err != nil {
				t.Fatal(err)
			}
			if err = database.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if opened, err := OpenExisting(t.Context(), path); err == nil || opened != nil {
				t.Fatal("incompatible schema accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("incompatible database changed: %v", err)
			}
		})
	}
	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "manager.db")
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if database, err := OpenExisting(t.Context(), path); err == nil || database != nil {
			t.Fatal("empty database accepted")
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("empty database initialized: %v", err)
		}
	})
}

func TestOpenExistingRejectsSymlinkAndCancelledContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manager.db")
	database, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if database, err := OpenExisting(ctx, path); err == nil || database != nil {
		t.Fatal("cancelled open accepted")
	}
	link := filepath.Join(filepath.Dir(path), "link.db")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if database, err := OpenExisting(t.Context(), link); err == nil || database != nil {
		t.Fatal("symlink database accepted")
	}
}
