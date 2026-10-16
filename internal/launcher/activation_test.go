package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestProtectedActivationBlockPreventsIndependentBoot(t *testing.T) {
	dir := t.TempDir()
	paths, _ := Paths(dir, "test")
	if err := prepare(paths); err != nil {
		t.Fatal(err)
	}
	if err := CheckActivationReady(t.Context(), dir, "test"); err != nil {
		t.Fatal(err)
	}
	if err := MarkActivationPending(t.Context(), dir, "test"); err != nil {
		t.Fatal(err)
	}
	verifierCalled := false
	err := run(t.Context(), Config{DataDir: dir, InstanceID: "test"}, nil, func(context.Context, Config, domain.LaunchSpec) error {
		verifierCalled = true
		return errors.New("unexpected verifier")
	})
	if domain.AsError(err).Code != domain.CodeUnavailable || verifierCalled {
		t.Fatalf("boot crossed pending guard: %v verifier=%v", err, verifierCalled)
	}
	if err = ClearActivationPending(t.Context(), dir, "test"); err != nil {
		t.Fatal(err)
	}
	if err = CheckActivationReady(t.Context(), dir, "test"); err != nil {
		t.Fatal(err)
	}
}
func TestMalformedAndUnsafeActivationBlocksFailClosed(t *testing.T) {
	for _, raw := range []string{"", `{}`, `{"version":1,"instance_id":"other"}`, `{"version":1,"instance_id":"test","memory":8192}`, `{"version":1,"instance_id":"test"} {}`, `{"version":1,"version":1,"instance_id":"test"}`} {
		t.Run(raw, func(t *testing.T) {
			dir := t.TempDir()
			paths, _ := Paths(dir, "test")
			if err := prepare(paths); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(paths.Server, ".manager")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(parent, activationPendingName)
			if err := os.WriteFile(marker, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if err := CheckActivationReady(t.Context(), dir, "test"); err == nil {
				t.Fatal("malformed blocker permitted launch")
			}
			if err := ClearActivationPending(t.Context(), dir, "test"); err == nil {
				t.Fatal("malformed blocker silently deleted")
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal(err)
			}
		})
	}
	if runtime.GOOS != "windows" {
		t.Run("protected_parent_link", func(t *testing.T) {
			dir := t.TempDir()
			paths, _ := Paths(dir, "test")
			if err := prepare(paths); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(paths.Server, ".manager")); err != nil {
				t.Fatal(err)
			}
			if err := CheckActivationReady(t.Context(), dir, "test"); err == nil {
				t.Fatal("linked parent permitted launch")
			}
		})
	}
}
