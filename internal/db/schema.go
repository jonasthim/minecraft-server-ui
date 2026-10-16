package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// LatestVersion is the embedded schema capability; it never opens a database.
func LatestVersion() int64 {
	var latest int64
	_ = fs.WalkDir(migrations, "migrations", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if ok && strings.HasSuffix(entry.Name(), ".sql") {
			v, e := strconv.ParseInt(prefix, 10, 64)
			if e == nil && v > latest {
				latest = v
			}
		}
		return nil
	})
	return latest
}

// CurrentVersion reads Goose's existing ledger without creating or migrating it.
func CurrentVersion(ctx context.Context, database *sql.DB) (int64, error) {
	var version int64
	if database == nil {
		return 0, fmt.Errorf("schema database unavailable")
	}
	if err := database.QueryRowContext(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied = 1 ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read existing schema version: %w", err)
	}
	if version < 1 {
		return 0, fmt.Errorf("database has no applied application schema")
	}
	return version, nil
}
