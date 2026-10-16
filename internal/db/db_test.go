package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndPersists(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "a & b", "manager.db")
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
