package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// This smoke test makes real official network requests and executes the verified
// Temurin JVM. It is skipped in all ordinary, network-free package test runs.
func TestOfficialTemurinSmoke(t *testing.T) {
	if os.Getenv("MINECRAFT_UI_RUNTIME_SMOKE") != "1" {
		t.Skip("set MINECRAFT_UI_RUNTIME_SMOKE=1 to download and execute official Temurin 17")
	}
	major := 17
	if selected := os.Getenv("MINECRAFT_UI_RUNTIME_SMOKE_MAJOR"); selected != "" {
		var err error
		major, err = strconv.Atoi(selected)
		if err != nil || !supportedMajor(major) {
			t.Fatalf("invalid smoke Java major %q", selected)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dir := t.TempDir()
	first, err := New(dir, nil).Ensure(ctx, major, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	fresh := New(dir, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("fresh-manager reuse must remain offline")
	})})
	cached, err := fresh.Get(ctx, first.ID)
	if err != nil || cached.ID != first.ID || cached.Executable != first.Executable {
		t.Fatalf("fresh Get failed: %+v %v", cached, err)
	}
	second, err := fresh.Ensure(ctx, major, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Executable != second.Executable {
		t.Fatal("verified runtime was not reused")
	}
	info, err := probeJava(ctx, first.Executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("verified official runtime: id=%s version=%s os=%s architecture=%s bytes=%d sha256=%s JVM vendor=%q runtime=%s arch=%s; fresh Get and offline Ensure reused", first.ID, first.Version, first.OS, first.Architecture, first.Artifact.Size, first.Artifact.Checksum.Value, info.Vendor, info.Version, info.Architecture)
}
