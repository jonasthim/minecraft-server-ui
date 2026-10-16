package runtime

import (
	"context"
	"os"
	"testing"
	"time"
)

// This smoke test makes real official network requests and executes the verified
// Temurin JVM. It is skipped in all ordinary, network-free package test runs.
func TestOfficialTemurinSmoke(t *testing.T) {
	if os.Getenv("MINECRAFT_UI_RUNTIME_SMOKE") != "1" {
		t.Skip("set MINECRAFT_UI_RUNTIME_SMOKE=1 to download and execute official Temurin 17")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dir := t.TempDir()
	first, err := New(dir, nil).Ensure(ctx, 17, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(dir, nil).Ensure(ctx, 17, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Executable != second.Executable {
		t.Fatal("verified runtime was not reused")
	}
	t.Logf("verified official runtime: id=%s version=%s os=%s architecture=%s sha256=%s", first.ID, first.Version, first.OS, first.Architecture, first.Artifact.Checksum.Value)
}
