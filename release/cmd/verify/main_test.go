package main

import (
	"bytes"
	"context"
	"testing"
)

func TestVerifyFlagsRequireExplicitArtifactIdentityAndTrust(t *testing.T) {
	for _, args := range [][]string{{}, {"--artifacts", "."}, {"--now", "bad"}, {"--schema", "0"}} {
		var out bytes.Buffer
		if e := run(context.Background(), args, &out); e == nil || out.Len() != 0 {
			t.Fatal("incomplete check reported success", e)
		}
	}
}
