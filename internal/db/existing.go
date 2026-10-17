package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
)

// OpenExisting opens a current-schema database without creating or migrating it.
// The caller validates its parent/sidecar paths and holds the manager lock until
// Close. Recovery must not silently turn an old database into a newer schema.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("existing database path: %w", err)
	}
	before, err := os.Lstat(absolute)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("existing database must be an ordinary file")
	}
	uri := FileURI(absolute)
	readonly, err := sql.Open("sqlite", uri+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open existing schema: %w", err)
	}
	readonly.SetMaxOpenConns(1)
	err = errors.Join(requireCurrentSchema(ctx, readonly), readonly.Close())
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(absolute)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("existing database identity changed")
	}
	connector, err := sqlite.NewConnector(uri + "?mode=rw&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)")
	if err != nil {
		return nil, fmt.Errorf("open existing database: %w", err)
	}
	database := sql.OpenDB(diskConnector{Connector: connector})
	database.SetMaxOpenConns(1)
	if err := requireCurrentSchema(ctx, database); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return database, nil
}

func requireCurrentSchema(ctx context.Context, database *sql.DB) error {
	version, err := CurrentVersion(ctx, database)
	if err != nil {
		return fmt.Errorf("read existing database schema: %w", err)
	}
	if version != LatestVersion() {
		return errors.New("existing database schema does not match this manager; use the matching installed version")
	}
	return nil
}
