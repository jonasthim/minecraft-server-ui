// Package db opens SQLite and applies embedded, ordered SQL migrations.
package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"github.com/pressly/goose/v3"
	"io"
	"io/fs"
	"modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
	database, err := open(ctx, u.String()+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)", true)
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
	return open(ctx, ":memory:?_pragma=foreign_keys(ON)", false)
}
func open(ctx context.Context, dsn string, durable bool) (*sql.DB, error) {
	var database *sql.DB
	var err error
	if durable {
		var connector driver.Connector
		connector, err = sqlite.NewConnector(dsn)
		if err == nil {
			database = sql.OpenDB(diskConnector{Connector: connector})
		}
	} else {
		database, err = sql.Open("sqlite", dsn)
	}
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

// Reapply DSN pragmas and verify their actual values before every physical disk
// connection enters the pool. In WAL mode FULL syncs each committed transaction;
// NORMAL can lose commits on power failure and cannot order our recovery journals
// against durable filesystem activation. OpenMemory keeps its original driver.
type diskConnector struct{ driver.Connector }

func (c diskConnector) Connect(ctx context.Context) (driver.Conn, error) {
	connection, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	if err = verifyDiskConnection(ctx, connection); err != nil {
		return nil, errors.Join(err, connection.Close())
	}
	return connection, nil
}
func verifyDiskConnection(ctx context.Context, connection driver.Conn) error {
	queryer, ok := connection.(driver.QueryerContext)
	if !ok {
		return fmt.Errorf("SQLite disk connection cannot verify required settings")
	}
	for _, setting := range []struct {
		name string
		want driver.Value
	}{
		{"journal_mode", "wal"},
		{"synchronous", int64(2)},
		{"foreign_keys", int64(1)},
		{"busy_timeout", int64(5000)},
	} {
		actual, err := readPragma(ctx, queryer, setting.name)
		if err != nil {
			return fmt.Errorf("verify SQLite %s: %w", setting.name, err)
		}
		if !reflect.DeepEqual(actual, setting.want) {
			return fmt.Errorf("SQLite %s=%v; require %v", setting.name, actual, setting.want)
		}
	}
	return nil
}
func readPragma(ctx context.Context, queryer driver.QueryerContext, name string) (value driver.Value, err error) {
	rows, err := queryer.QueryContext(ctx, "PRAGMA "+name, nil)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	if len(rows.Columns()) != 1 {
		return nil, fmt.Errorf("unexpected pragma columns")
	}
	values := make([]driver.Value, 1)
	if err = rows.Next(values); err != nil {
		return nil, err
	}
	value = values[0]
	if err = rows.Next(values); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected pragma rows")
		}
		return nil, err
	}
	return value, nil
}
