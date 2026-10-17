package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIRejectsMissingConflictingOrInvalidSigningModes(t *testing.T) {
	for _, args := range [][]string{{}, {"--development", "--signing-key-file", "-"}, {"--development", "--published-at", "not-a-time"}, {"--signing-key-file", "-", "--published-at", "2026-10-17T12:00:00Z"}} {
		var out bytes.Buffer
		if err := run(context.Background(), args, strings.NewReader("PRIVATE-TEST-SECRET"), &out); err == nil || strings.Contains(err.Error(), "PRIVATE-TEST-SECRET") {
			t.Fatal("invalid CLI accepted or secret leaked")
		}
		if out.Len() != 0 {
			t.Fatal("failure reported success", out.String())
		}
	}
}
