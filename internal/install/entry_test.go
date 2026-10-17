package install

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRunInstallerCommandLeavesOtherCommandsUntouched(t *testing.T) {
	var output bytes.Buffer
	handled, err := RunInstallerCommand(context.Background(), []string{"serve"}, &output)
	if handled || err != nil || output.Len() != 0 {
		t.Fatalf("handled=%v err=%v output=%q", handled, err, output.String())
	}
}

func TestRunInstallerCommandRejectsMalformedArgumentsBeforePlatformBackend(t *testing.T) {
	var output bytes.Buffer
	handled, err := RunInstallerCommand(context.Background(), []string{"install", "apply", "--prepared", "relative"}, &output)
	if !handled || err == nil || output.Len() != 0 {
		t.Fatalf("handled=%v err=%v output=%q", handled, err, output.String())
	}
}

func TestParseInstallerCommandStrictArguments(t *testing.T) {
	valid := []string{"install", "apply", "--prepared", "/tmp/prepared", "--trust-file", "/tmp/trust", "--version", "v1.2.3", "--repair", "--allow-development"}
	command, err := parseInstallerCommand(valid)
	if err != nil || command.kind != installerApply || command.options.Version != "v1.2.3" || !command.options.Repair || !command.options.AllowDevelopment {
		t.Fatalf("command=%+v err=%v", command, err)
	}
	cases := [][]string{
		{"install"},
		{"install", "apply", "--prepared", "/tmp/a", "--trust-file", "/tmp/b", "--version", "v1.2.3", "extra"},
		{"install", "apply", "--prepared", "/tmp/a", "--prepared", "/tmp/b", "--trust-file", "/tmp/t", "--version", "v1.2.3"},
		{"install", "apply", "--prepared", "relative", "--trust-file", "/tmp/t", "--version", "v1.2.3"},
		{"install", "apply", "--prepared", "/tmp/a", "--trust-file", "/tmp/t", "--version", "v1.2.3-rc1"},
		{"install", "apply", "--prepared", "/tmp/a", "--trust-file", "/tmp/t", "--version", "v1.2.3", "--repair", "--repair"},
		{"install", "check", "extra"},
		{"install", "uninstall", "--repair"},
		{"install", "worker", "../../etc/passwd"},
		{"install", "recover-worker", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
	}
	for _, args := range cases {
		if _, err := parseInstallerCommand(args); err == nil {
			t.Errorf("accepted invalid args %q", args)
		}
	}
}

func TestInstallerCommandValidatesBeforeAuthorityAndWritesOneBoundedResponse(t *testing.T) {
	called := false
	var output bytes.Buffer
	backend := installerEntryBackend{
		authorize: func() error { called = true; return errors.New("secret detail") },
		run: func(context.Context, installerCommand) (any, error) {
			t.Fatal("run after denied authority")
			return nil, nil
		},
	}
	if err := runInstallerCommand(context.Background(), installerCommand{kind: installerCheck}, &output, backend); err == nil || !called || output.Len() != 0 {
		t.Fatalf("err=%v called=%v output=%q", err, called, output.String())
	}

	called = false
	backend.authorize = func() error { called = true; return nil }
	backend.run = func(context.Context, installerCommand) (any, error) {
		return CheckReport{State: "absent"}, nil
	}
	if err := runInstallerCommand(context.Background(), installerCommand{kind: installerCheck}, &output, backend); err != nil {
		t.Fatal(err)
	}
	if !called || output.String() != "{\"state\":\"absent\",\"coherent\":false,\"pending\":false}\n" {
		t.Fatalf("called=%v output=%q", called, output.String())
	}
}

func TestInstallerCommandDoesNotExposeAuthorizationError(t *testing.T) {
	var output bytes.Buffer
	err := runInstallerCommand(context.Background(), installerCommand{kind: installerCheck}, &output, installerEntryBackend{
		authorize: func() error { return errors.New("sensitive path /secret/token") },
		run:       func(context.Context, installerCommand) (any, error) { return nil, nil },
	})
	if err == nil || err.Error() != "installer command requires local administrator authority" {
		t.Fatalf("unsafe authorization error: %v", err)
	}
}

func TestInstallerCheckAcceptsOnlyNoncoherentNonpendingRetainedState(t *testing.T) {
	image := &Image{Version: "v1.2.3", Schema: 7, SHA256: strings.Repeat("a", 64)}
	retained := CheckReport{State: "retained", Coherent: false, Pending: false, Image: image}
	if !validInstallerCheckReport(retained) {
		t.Fatal("valid retained report rejected")
	}
	coherent := retained
	coherent.Coherent = true
	if validInstallerCheckReport(coherent) {
		t.Fatal("coherent retained report accepted")
	}
	pending := retained
	pending.Pending = true
	if validInstallerCheckReport(pending) {
		t.Fatal("pending retained report accepted")
	}
	invalidImage := retained
	invalidImage.Image = &Image{Version: "v1.2.3", Schema: 0, SHA256: strings.Repeat("a", 64)}
	if validInstallerCheckReport(invalidImage) {
		t.Fatal("retained report with invalid image accepted")
	}
}
