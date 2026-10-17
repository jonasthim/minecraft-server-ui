package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
)

func TestAdminResetRejectsUnsafeArgumentsBeforeInput(t *testing.T) {
	for _, args := range [][]string{nil, {"--username", "admin"}, {"--password-stdin"}, {"--username", "admin", "--password", "do-not-echo-secret"}, {"--username", "admin", "--password-stdin", "do-not-echo-secret"}, {"--username", "admin", "--username", "other", "--password-stdin"}, {"--username", "admin", "--password-stdin", "--password-stdin"}, {"--username", "--password-stdin"}, {"--username=admin", "--password-stdin=false"}} {
		t.Run("invalid", func(t *testing.T) {
			var out bytes.Buffer
			r := &recoveryReadRecorder{}
			err := runAdminReset(t.Context(), args, r, &out)
			if err == nil {
				t.Fatal("accepted unsafe arguments")
			}
			if strings.Contains(err.Error(), "do-not-echo-secret") || strings.Contains(out.String(), "do-not-echo-secret") {
				t.Fatal("echoed argument secret")
			}
			if r.read {
				t.Fatal("read password on argument error")
			}
		})
	}
}

type recoveryReadRecorder struct{ read bool }

func (r *recoveryReadRecorder) Read([]byte) (int, error) { r.read = true; return 0, io.EOF }

func TestAdminResetUsesConfiguredDirectoryWithoutCreatingState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var out bytes.Buffer
	if err := runAdminReset(t.Context(), []string{"--username=admin", "--password-stdin"}, strings.NewReader("test-password"), &out); err == nil {
		t.Fatal("accepted uninitialized manager")
	}
	if out.Len() != 0 {
		t.Fatal("reported success on failure")
	}
}

func TestAdminResetCommandSuccess(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state #?&")
	database, err := db.Open(t.Context(), filepath.Join(dir, "manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	// Current-schema disposable account; public setup/login behavior is covered
	// by internal/recovery's real auth-service fixture.
	if _, err = database.Exec(`INSERT INTO users(username,password_hash,role,created_at,updated_at) VALUES('admin','existing-local-hash','admin','2026-10-17T00:00:00Z','2026-10-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINECRAFT_UI_DATA_DIR", dir)
	var out bytes.Buffer
	if err = runAdminReset(t.Context(), []string{"--password-stdin", "--username", " ADMIN "}, strings.NewReader("private-new-password\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "sessions and personal access tokens revoked") || strings.Contains(out.String(), "private-new-password") {
		t.Fatalf("unsafe or missing result: %q", out.String())
	}
}
