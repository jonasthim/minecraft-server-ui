package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenMigratesAndPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a # & % b", "manager.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('main','Main','vanilla','1.21.4',21,2048,25565)`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var n int
	if err := database.QueryRowContext(ctx, "SELECT count(*) FROM instances").Scan(&n); err != nil || n != 1 {
		t.Fatalf("count=%d error=%v", n, err)
	}
	var fk int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys=%d error=%v", fk, err)
	}
}
func TestOpenMemoryIsIsolated(t *testing.T) {
	ctx := context.Background()
	a, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := a.ExecContext(ctx, "INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('test','Test','vanilla','26.3',25,2048,25565)"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.QueryRowContext(ctx, "SELECT count(*) FROM instances").Scan(&n); err != nil || n != 0 {
		t.Fatalf("count=%d error=%v", n, err)
	}
}

func TestOnDiskDurabilityAcrossConnectionReplacementAndReopen(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "durable & journal", "manager.db")
	database, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if database != nil {
			database.Close()
		}
	}()
	checkDiskSettings(t, database)
	if _, err = database.ExecContext(ctx, `INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('durable','Durable','vanilla','1.20.1',17,1024,25565)`); err != nil {
		t.Fatal(err)
	}
	first, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var previous any
	if err = first.Raw(func(connection any) error { previous = connection; return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatalf("physical connection was not discarded: %v", err)
	}
	_ = first.Close()
	second, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Raw(func(connection any) error {
		if connection == previous {
			t.Error("expected new physical connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	checkDiskSettings(t, database)
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	checkDiskSettings(t, database)
	var count int
	if err = database.QueryRowContext(ctx, `SELECT COUNT(*) FROM instances WHERE id='durable'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reopen lost ordinary committed row: count=%d err=%v", count, err)
	}
}
func checkDiskSettings(t *testing.T, database *sql.DB) {
	t.Helper()
	if got := database.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("connection contract changed: %d", got)
	}
	var journal string
	if err := database.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal mode=%s err=%v", journal, err)
	}
	for pragma, want := range map[string]int{"synchronous": 2, "foreign_keys": 1, "busy_timeout": 5000} {
		var got int
		if err := database.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%d want%d err=%v", pragma, got, want, err)
		}
	}
}

func TestDiskConnectorRefusesIncorrectActualSettings(t *testing.T) {
	for _, tc := range []struct{ name, pragmas string }{
		{"journal_mode", "journal_mode(DELETE)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"},
		{"synchronous", "journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"},
		{"foreign_keys", "journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(OFF)&_pragma=busy_timeout(5000)"},
		{"busy_timeout", "journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(1)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.db")
			database, err := open(t.Context(), FileURI(path)+"?_pragma="+tc.pragmas, true)
			if database != nil {
				database.Close()
				t.Fatal("unexpected noncompliant database returned")
			}
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("wrong %s setting was not refused: %v", tc.name, err)
			}
		})
	}
}

func TestFileURIKeepsWindowsDriveInPathAndEscapesMetacharacters(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"C:/Users/Runner/my # & %/manager.db", "file:///C:/Users/Runner/my%20%23%20&%20%25/manager.db"},
		{"D:/question?name/manager.db", "file:///D:/question%3Fname/manager.db"},
		{"/var/lib/my # & %/manager.db", "file:///var/lib/my%20%23%20&%20%25/manager.db"},
	} {
		if got := FileURI(tc.path); got != tc.want {
			t.Errorf("FileURI(%q)=%q, want %q", tc.path, got, tc.want)
		}
	}
}
func TestMemoryOpeningRetainsMemoryJournalAndSingleConnection(t *testing.T) {
	database, err := OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var journal string
	if err = database.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&journal); err != nil || journal != "memory" {
		t.Fatalf("memory semantics changed: journal=%s %v", journal, err)
	}
	if got := database.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("memory connection contract changed: %d", got)
	}
}
