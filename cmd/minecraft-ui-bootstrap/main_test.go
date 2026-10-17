package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/release/bootstrap"
)

func TestVersionCommand(t *testing.T) {
	var out, err bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &err); code != 0 || out.String() != "minecraft-ui-bootstrap 0.0.0-dev\n" || err.Len() != 0 {
		t.Fatal(code, out.String(), err.String())
	}
}

func TestPrepareInstallAuthenticatesBeforeDispatchAndUsesResolvedVersion(t *testing.T) {
	for _, failPrepare := range []bool{false, true} {
		var events []string
		var installArgs []string
		backend := bootstrapBackend{
			canInstall: func() bool { return true },
			prepare: func(_ context.Context, o bootstrap.Options) (bootstrap.Receipt, error) {
				events = append(events, "prepare")
				if o.Version != "latest" {
					t.Fatal(o.Version)
				}
				if failPrepare {
					return bootstrap.Receipt{}, errors.New("fixture authentication refused")
				}
				return bootstrap.Receipt{Version: "v2.3.4"}, nil
			},
			installer: func(_ context.Context, args []string, out io.Writer) (bool, error) {
				if args[0] != "install" {
					return false, nil
				}
				events = append(events, "install")
				installArgs = append([]string(nil), args...)
				_, err := io.WriteString(out, "{\"outcome\":\"applied\"}\n")
				return true, err
			},
		}
		var out, errOut bytes.Buffer
		code := runWithBackend(context.Background(), []string{"prepare", "--repository", "jonasthim/minecraft-server-ui", "--version", "latest", "--trust-file", "/trusted/public.json", "--output", "/private/prepared", "--install", "--repair", "--allow-development"}, &out, &errOut, backend)
		if failPrepare {
			if code == 0 || !reflect.DeepEqual(events, []string{"prepare"}) || out.Len() != 0 {
				t.Fatal(code, events, out.String())
			}
			continue
		}
		want := []string{"install", "apply", "--prepared", "/private/prepared", "--trust-file", "/trusted/public.json", "--version", "v2.3.4", "--repair", "--allow-development"}
		if code != 0 || !reflect.DeepEqual(events, []string{"prepare", "install"}) || !reflect.DeepEqual(installArgs, want) || out.String() != "{\"outcome\":\"applied\"}\n" {
			t.Fatal(code, events, installArgs, out.String(), errOut.String())
		}
	}
}

func TestPrepareInstallRejectsAuthorityBeforeAcquisition(t *testing.T) {
	backend := bootstrapBackend{canInstall: func() bool { return false }, installer: func(context.Context, []string, io.Writer) (bool, error) { return false, nil }, prepare: func(context.Context, bootstrap.Options) (bootstrap.Receipt, error) {
		t.Fatal("unauthorized acquisition")
		return bootstrap.Receipt{}, nil
	}}
	var out, errOut bytes.Buffer
	if code := runWithBackend(context.Background(), []string{"prepare", "--repository", "jonasthim/minecraft-server-ui", "--install"}, &out, &errOut, backend); code == 0 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
}
func TestCommandErrorsBoundedAndSecretFree(t *testing.T) {
	base := t.TempDir()
	trust := filepath.Join(base, "trust.json")
	if e := os.WriteFile(trust, []byte("SECRET malformed public trust"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, args := range [][]string{nil, {"unknown", "SECRET"}, {"prepare", "--url=https://bad.invalid/?token=SECRET"}, {"prepare", "--repository", "example/repo", "--version", "v1.2.3?token=SECRET"}, {"prepare", "--repository", "example/repo", "--version", "v1.2.3", "--trust-file", trust, "--output", filepath.Join(base, "new")}, {"prepare", "--version", "v1.2.3", "--now", "SECRET"}, {"version", "SECRET"}} {
		var out, err bytes.Buffer
		code := run(context.Background(), args, &out, &err)
		if code == 0 || out.Len() != 0 || err.Len() == 0 || err.Len() > 512 || strings.Contains(err.String(), "SECRET") {
			t.Fatal(code, out.String(), err.String())
		}
	}
}
