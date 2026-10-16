package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestLaunchDispatchAcceptsInstanceFlags(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"launch", "--data-dir", t.TempDir(), "--instance", "test"}, &out)
	if err == nil || !strings.Contains(err.Error(), "read launch specification") {
		t.Fatalf("launch was not dispatched to the launcher: %v", err)
	}
}
func TestLaunchDispatchRejectsUnsafeInstance(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"launch", "--data-dir", t.TempDir(), "--instance", "../escape"}, &out)
	if err == nil || !strings.Contains(err.Error(), "instance id") {
		t.Fatalf("unsafe launch did not reach validation: %v", err)
	}
}
