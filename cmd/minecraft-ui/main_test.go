package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionAndUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "minecraft-ui ") {
		t.Fatal(out.String())
	}
	if err := run(context.Background(), []string{"unknown"}, &out); err == nil {
		t.Fatal("unknown command accepted")
	}
}
