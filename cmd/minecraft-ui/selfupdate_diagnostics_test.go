package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

func TestSchemaProbeDiagnosticChild(t *testing.T) {
	mode := os.Getenv("MUI_SCHEMA_DIAGNOSTIC_FIXTURE")
	if mode == "" {
		return
	}
	version = "v1.2.3"
	if mode == "encode" {
		_ = os.Stdout.Close()
	}
	args := []string{"update-schema"}
	if mode == "capability" {
		args = []string{"update-capability"}
	} else if mode == "bad_arguments" {
		args = append(args, "private-secret")
	}
	_, err := runSelfUpdateProbe(context.Background(), args, os.Stdout)
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestSchemaProbeEntryIsSpecificAndDoesNotChangeOutput(t *testing.T) {
	for _, mode := range []string{"success", "capability", "bad_arguments"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			if mode == "success" {
				database, err := db.Open(context.Background(), filepath.Join(dir, "manager.db"))
				if err != nil {
					t.Fatal(err)
				}
				database.Close()
			}
			command := exec.Command(os.Args[0], "-test.run=^TestSchemaProbeDiagnosticChild$")
			command.Env = append(os.Environ(), "MUI_SCHEMA_DIAGNOSTIC_FIXTURE="+mode, "MINECRAFT_UI_DATA_DIR="+dir,
				"MINECRAFT_UI_ADDR=127.0.0.1:8080", "MINECRAFT_UI_BASE_URL=http://127.0.0.1:8080", "MINECRAFT_UI_INSECURE_COOKIES=true")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if (err != nil) != (mode == "bad_arguments") {
				t.Fatal("command outcome changed", mode, err)
			}
			want := ""
			if mode == "success" {
				want = "native-update-schema-diagnostic:child:entered\n"
			}
			if stderr.String() != want {
				t.Fatalf("entry stream = %q, want %q", stderr.String(), want)
			}
			if mode == "bad_arguments" {
				if stdout.Len() != 0 {
					t.Fatal("invalid arguments produced output")
				}
			} else {
				var capability selfupdate.Capability
				if json.Unmarshal(stdout.Bytes(), &capability) != nil || capability.Schema != db.LatestVersion() || capability.Version != "v1.2.3" {
					t.Fatal("typed probe output changed", stdout.String())
				}
			}
		})
	}
}

func TestSchemaProbeReportsOnlyFixedFailureBoundary(t *testing.T) {
	for _, tc := range []struct{ mode, marker string }{
		{"config", "config:other"},
		{"schema_missing", "schema_read:not_exist"},
		{"schema_sqlite", "schema_read:sqlite:1"},
		{"challenge_invalid", "challenge_read:other"},
		{"challenge_version", "challenge_version:other"},
		{"actor_sqlite", "actor_read:sqlite:1"},
		{"encode", "encode:other"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "manager.db")
			if tc.mode == "schema_sqlite" {
				database, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = database.Exec("CREATE TABLE private_secret(value TEXT)")
				if err != nil {
					t.Fatal(err)
				}
				database.Close()
			} else if tc.mode != "config" && tc.mode != "schema_missing" {
				database, err := db.Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				if tc.mode == "actor_sqlite" {
					if _, err := database.Exec("DROP TABLE users"); err != nil {
						t.Fatal(err)
					}
				}
				database.Close()
			}
			if strings.HasPrefix(tc.mode, "challenge_") || tc.mode == "actor_sqlite" {
				control := filepath.Join(dir, "selfupdate", "control")
				if err := os.MkdirAll(control, 0700); err != nil {
					t.Fatal(err)
				}
				challenge := selfupdate.Challenge{Format: 1, Nonce: strings.Repeat("a", 64), FromVersion: "v1.2.3", ToVersion: "v1.2.4", ActorID: 1, ExpiresAt: time.Now().Add(9 * time.Minute)}
				if tc.mode == "challenge_version" {
					challenge.FromVersion = "v1.2.2"
				}
				body, _ := json.Marshal(challenge)
				if tc.mode == "challenge_invalid" {
					body = []byte("private-secret")
				}
				if err := os.WriteFile(filepath.Join(control, "challenge.json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command(os.Args[0], "-test.run=^TestSchemaProbeDiagnosticChild$")
			command.Env = append(os.Environ(), "MUI_SCHEMA_DIAGNOSTIC_FIXTURE="+tc.mode, "MINECRAFT_UI_DATA_DIR="+dir,
				"MINECRAFT_UI_ADDR=127.0.0.1:8080", "MINECRAFT_UI_BASE_URL=http://127.0.0.1:8080", "MINECRAFT_UI_INSECURE_COOKIES=true")
			if tc.mode == "config" {
				command.Env = append(command.Env, "MINECRAFT_UI_ADDR=private-secret")
			}
			body, err := command.CombinedOutput()
			if err == nil {
				t.Fatal("invalid fixture unexpectedly passed")
			}
			want := "native-update-schema-diagnostic:child:entered\nnative-update-schema-diagnostic:" + tc.marker + "\n"
			if string(body) != want {
				t.Fatalf("diagnostic = %q, want %q", body, want)
			}
		})
	}
}

func TestSchemaProbeFailureClassifiesWrappedErrorsWithoutCauseText(t *testing.T) {
	for _, tc := range []struct {
		cause error
		want  string
	}{
		{os.ErrPermission, "permission"},
		{os.ErrNotExist, "not_exist"},
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "deadline"},
		{errors.New("private-secret"), "other"},
	} {
		var diagnostics bytes.Buffer
		writeSchemaProbeFailure(&diagnostics, "schema_read", fmt.Errorf("private-secret: %w", tc.cause))
		if got, want := diagnostics.String(), "native-update-schema-diagnostic:schema_read:"+tc.want+"\n"; got != want {
			t.Fatalf("diagnostic = %q, want %q", got, want)
		}
	}
	var diagnostics bytes.Buffer
	writeSchemaProbeFailure(&diagnostics, "private-secret", os.ErrPermission)
	writeSchemaProbeFailure(&diagnostics, "schema_read", nil)
	if diagnostics.Len() != 0 {
		t.Fatal("unknown stage or nil error emitted a diagnostic", diagnostics.String())
	}
}
