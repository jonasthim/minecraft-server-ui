package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	var out, err bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &out, &err); code != 0 || out.String() != "minecraft-ui-bootstrap 0.0.0-dev\n" || err.Len() != 0 {
		t.Fatal(code, out.String(), err.String())
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
