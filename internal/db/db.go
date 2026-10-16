// Package db opens SQLite and applies embedded, ordered SQL migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"github.com/pressly/goose/v3"
	"io/fs"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, path string) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("database path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	// URL encoding preserves paths containing #, ?, &, spaces and Windows drives.
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	database, err := open(ctx, u.String()+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Chmod(absolute+suffix, 0600); err != nil && !os.IsNotExist(err) {
			database.Close()
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}
	return database, nil
}
func OpenMemory(ctx context.Context) (*sql.DB, error) {
	return open(ctx, ":memory:?_pragma=foreign_keys(ON)")
}
func open(ctx context.Context, dsn string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// One connection preserves isolated in-memory databases and serializes writers.
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := Migrate(ctx, database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}
func Migrate(ctx context.Context, database *sql.DB) error {
	source, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migration source: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, database, source, goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}
