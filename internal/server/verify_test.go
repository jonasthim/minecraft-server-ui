package server

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestLaunchRejectsEveryEnvironmentOverrideBeforeProviderAccess(t *testing.T) {
	for _, key := range []string{"JAVA_TOOL_OPTIONS", "LD_AUDIT", "PATH", "TZ", "APPROVED_LATER"} {
		if err := VerifyLaunch(context.Background(), t.TempDir(), "test", domain.LaunchSpec{Env: map[string]string{key: "value"}}); err == nil {
			t.Fatalf("unapproved environment override accepted: %s", key)
		}
	}
}
