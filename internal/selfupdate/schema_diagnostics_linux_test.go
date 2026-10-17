//go:build linux

package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestLinuxSchemaProbeDiagnosticChild(t *testing.T) {
	mode := os.Getenv("MUI_LINUX_SCHEMA_DIAGNOSTIC_FIXTURE")
	if mode == "" {
		return
	}
	switch mode {
	case "exit":
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		fmt.Fprintln(os.Stderr, "private-secret error path actor nonce")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:schema_read:sqlite:14 private-secret")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:schema_read:sqlite:14")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:actor_read:permission")
		os.Exit(37)
	case "wrapper_session":
		fmt.Fprintln(os.Stderr, "runuser: cannot open session: Permission denied private-secret")
		fmt.Fprintln(os.Stderr, "runuser: cannot open session: Permission denied")
		os.Exit(1)
	case "wrapper_exec":
		fmt.Fprintln(os.Stderr, "runuser: failed to execute /private-secret: Permission denied")
		fmt.Fprintln(os.Stderr, "runuser: failed to execute /usr/local/bin/minecraft-ui: Permission denied")
		os.Exit(1)
	case "entry_only":
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		os.Exit(1)
	case "stderr_overflow":
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:schema_read:permission")
		fmt.Fprint(os.Stderr, strings.Repeat("private-secret", 1000))
		os.Exit(1)
	case "signal":
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		select {}
	case "invalid_json":
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		fmt.Fprint(os.Stdout, "private-secret")
	case "overflow":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 8193))
	case "success":
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		fmt.Fprintln(os.Stderr, "private-secret")
		fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:schema_read:permission")
		fmt.Fprint(os.Stdout, `{"format":1,"version":"v1.2.3","schema":20,"actor_authorized":true}`)
	}
	os.Exit(0)
}

func TestLinuxSchemaProbeClassifiesActualProcessOutcomes(t *testing.T) {
	for _, tc := range []struct{ mode, want, errText string }{
		{"exit", "child:entered\nschema_read:sqlite:14\ncommand:exit:37\n", "manager-account schema probe failed"},
		{"wrapper_session", "runuser_session:permission\ncommand:exit:1\n", "manager-account schema probe failed"},
		{"wrapper_exec", "runuser_exec:eacces\ncommand:exit:1\n", "manager-account schema probe failed"},
		{"entry_only", "child:entered\ncommand:exit:1\n", "manager-account schema probe failed"},
		{"signal", "command:signal:15\n", "manager-account schema probe failed"},
		{"stderr_overflow", "command:stderr_overflow\ncommand:exit:1\n", "manager-account schema probe failed"},
		{"invalid_json", "child:entered\ncommand:invalid_json\n", "invalid manager schema probe"},
		{"overflow", "command:output_overflow\n", "manager-account schema probe failed"},
		{"missing", "command:launch_not_exist\n", "manager-account schema probe failed"},
		{"permission", "command:launch_permission\n", "manager-account schema probe failed"},
		{"canceled", "command:context_canceled\n", "manager-account schema probe failed"},
		{"deadline", "command:context_deadline\n", "manager-account schema probe failed"},
		{"success", "", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			ctx := context.Background()
			if tc.mode == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			} else if tc.mode == "deadline" {
				deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				ctx = deadline
			}
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLinuxSchemaProbeDiagnosticChild$")
			command.Env = append(os.Environ(), "MUI_LINUX_SCHEMA_DIAGNOSTIC_FIXTURE="+tc.mode)
			if tc.mode == "missing" || tc.mode == "permission" {
				path := filepath.Join(t.TempDir(), "private-secret")
				if tc.mode == "permission" {
					if err := os.WriteFile(path, []byte("private-secret"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				command = exec.CommandContext(ctx, path)
			}
			var diagnostics bytes.Buffer
			capability, err := runLinuxSchemaProbe(ctx, command, &diagnostics)
			if tc.errText == "" {
				if err != nil || capability.Version != "v1.2.3" || capability.Schema != 20 || capability.ActorAuthorized == nil || !*capability.ActorAuthorized {
					t.Fatal("successful probe changed", capability, err)
				}
			} else if err == nil || err.Error() != tc.errText {
				t.Fatal("original failure changed", err)
			}
			want := ""
			for _, line := range strings.Split(strings.TrimSuffix(tc.want, "\n"), "\n") {
				if line != "" {
					want += "native-update-schema-diagnostic:" + line + "\n"
				}
			}
			if diagnostics.String() != want {
				t.Fatalf("diagnostics = %q, want %q", diagnostics.String(), want)
			}
		})
	}
}

func TestLinuxSchemaProbeChildMarkerRequiresExactBoundedGrammar(t *testing.T) {
	for _, marker := range []string{
		"schema_read:sqlite:1", "actor_read:sqlite:65535", "config:other",
		"schema_read:permission", "challenge_read:not_exist", "schema_read:canceled",
		"schema_read:deadline", "encode:other", "challenge_version:other",
	} {
		if !validSchemaProbeDiagnostic("native-update-schema-diagnostic:" + marker) {
			t.Error("valid child marker refused", marker)
		}
	}
	for _, marker := range []string{
		"schema_read:sqlite:0", "schema_read:sqlite:65536", "schema_read:sqlite:-1",
		"schema_read:sqlite:+1", "schema_read:sqlite:01", "schema_read:sqlite:private-secret",
		"schema_read:permission:1", "private-secret:other", "schema_read:private-secret",
		"command:exit:1", "schema_read:sqlite:1 private-secret", "schema_read:sqlite:1\r",
		"schema_read:permission\nprivate-secret",
	} {
		if validSchemaProbeDiagnostic("native-update-schema-diagnostic:" + marker) {
			t.Error("unsafe child marker accepted", marker)
		}
	}
}

func TestRunuserDiagnosticMatchesOnlyExactSourceMessages(t *testing.T) {
	for input, suffix := range map[string]string{
		"runuser: cannot open session: Permission denied":                                            "runuser_session:permission",
		"runuser: failed to establish user credentials: Failure setting user credentials":            "runuser_credentials:credentials",
		"runuser: cannot set groups: Operation not permitted":                                        "runuser_groups:eperm",
		"runuser: cannot set group id: Permission denied":                                            "runuser_gid:eacces",
		"runuser: cannot set user id: Operation not permitted":                                       "runuser_uid:eperm",
		"runuser: failed to execute /usr/local/bin/minecraft-ui.previous: No such file or directory": "runuser_exec:enoent",
		"runuser: System error": "runuser_auth:system",
	} {
		if got, want := classifyRunuserFailure(input), "native-update-schema-diagnostic:"+suffix; got != want {
			t.Errorf("marker = %q, want %q", got, want)
		}
	}
	for _, input := range []string{
		"runuser: cannot open session: Permission denied private-secret",
		"runuser: cannot open session: Permission denied\nprivate-secret",
		"runuser: cannot open session: Permission denied\r",
		"private-secret runuser: cannot open session: Permission denied",
		"runuser: private-secret: Permission denied",
		"runuser: cannot open session: private-secret",
		"runuser: failed to execute /usr/local/bin/../bin/minecraft-ui: Permission denied",
		"runuser: failed to execute /private-secret: Permission denied",
	} {
		if marker := classifyRunuserFailure(input); marker != "" {
			t.Error("unknown or extended message produced a marker", marker)
		}
	}
}
