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

func TestNativeInspectDispatchRejectsCallerSelectedProcessOrPath(t *testing.T) {
	for _, args := range [][]string{{"native-inspect", "--pid", "1"}, {"native-inspect", "--instance", "../manager"}, {"native-inspect", "--instance", "test", "--data-dir", "/tmp"}, {"native-inspect", "--instance", "test", "--pid", "1"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err == nil || out.Len() != 0 {
			t.Fatalf("unsafe native inspection arguments accepted %q: %v", args, err)
		}
	}
}
