package db

import (
	"context"
	"testing"
)

func TestSchemaVersionMatchesEmbeddedMigrations(t *testing.T) {
	ctx := context.Background()
	database, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	got, err := CurrentVersion(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if got != LatestVersion() {
		t.Fatalf("current %d latest %d", got, LatestVersion())
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := CurrentVersion(ctx, database); err == nil {
		t.Fatal("canceled schema read succeeded")
	}
}
