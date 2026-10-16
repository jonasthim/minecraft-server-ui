package launcher

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestContentTransactionMarkerBlocksIndependentBoot(t *testing.T) {
	dir := t.TempDir()
	paths, e := Paths(dir, "test")
	if e != nil {
		t.Fatal(e)
	}
	if e = prepare(paths); e != nil {
		t.Fatal(e)
	}
	if e = CheckActivationReady(t.Context(), dir, "test"); e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(paths.State, "content-transaction.json")
	for _, raw := range []string{"", `garbage`, `{"version":1,"instance_id":"test","preview_id":"01234567890123456789012345678901"}`} {
		if e = os.WriteFile(marker, []byte(raw), 0600); e != nil {
			t.Fatal(e)
		}
		if e = CheckActivationReady(t.Context(), dir, "test"); domain.AsError(e).Code != domain.CodeUnavailable {
			t.Fatal("pending content admitted launcher", e)
		}
	}
	verifierCalled := false
	e = run(t.Context(), Config{DataDir: dir, InstanceID: "test"}, nil, func(context.Context, Config, domain.LaunchSpec) error {
		verifierCalled = true
		return errors.New("unexpected verifier")
	})
	if domain.AsError(e).Code != domain.CodeUnavailable || verifierCalled {
		t.Fatal("launcher crossed content blocker", e, verifierCalled)
	}
	if e = os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	if e = CheckActivationReady(t.Context(), dir, "test"); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(t.TempDir(), marker); e != nil {
		t.Skip(e)
	}
	if e = CheckActivationReady(t.Context(), dir, "test"); e == nil {
		t.Fatal("linked content marker admitted launch")
	}
}
