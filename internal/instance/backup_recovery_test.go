package instance

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"os"
	"path/filepath"
	"testing"
)

type backupRecoveryGuard struct {
	q      *jobs.Service
	native MutationGuard
	calls  int
}

func (g *backupRecoveryGuard) WithStoppedForRecovery(ctx context.Context, id string, fn func() error) error {
	return g.q.WithPausedInstance(ctx, id, func() error { g.calls++; return g.native.WithStopped(ctx, id, fn) })
}
func TestBackupRecoveryOriginalSessionAndPersistedProperties(t *testing.T) {
	s, p, native := setup(t)
	d := draft("alpha", 25565)
	d.Config.Extra = map[string]string{"custom": "current", "delete-me": "remove"}
	if _, err := s.Create(t.Context(), admin, d); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(s.db, nil)
	q.Register("retained", func(context.Context, *jobs.Execution) error { return nil })
	if err := q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	if _, err := q.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "retained", RequestedBy: 1}); err != nil {
		t.Fatal(err)
	}
	guard := &backupRecoveryGuard{q: q, native: native}
	s = New(s.db, p, native, WithRecoveryMutationGuard(guard))
	dirs, _ := p.For("alpha")
	os.WriteFile(filepath.Join(dirs.Server, "server.properties"), []byte("custom=restored\nnew-key=new-value\nmax-players=99\nmanagement-server-secret=private\n"), 0600)
	var retained *StoppedSession
	err := s.WithStoppedRecovery(t.Context(), "alpha", func(session *StoppedSession) error {
		retained = session
		copy := *session
		if err := copy.Materialize(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
			return errors.New("copied recovery session accepted")
		}
		return session.MaterializeRestoredProperties(t.Context())
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Config.Extra["custom"] != "restored" || detail.Config.Extra["new-key"] != "new-value" || detail.Config.Extra["delete-me"] != "" || len(detail.Config.Extra) != 2 {
		t.Fatalf("ordinary properties not restored: %v", detail.Config.Extra)
	}
	if detail.Config.MaxPlayers != d.Config.MaxPlayers || detail.Instance.GamePort != 25565 {
		t.Fatal("typed settings changed")
	}
	if err = retained.Materialize(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("expired recovery session accepted")
	}
	if guard.calls != 1 {
		t.Fatal("nested guard")
	}
	if err = q.Resume(); err != nil {
		t.Fatal(err)
	}
	if err = s.WithStoppedRecovery(t.Context(), "alpha", func(*StoppedSession) error { return nil }); err == nil {
		t.Fatal("recovery session admitted after Resume")
	}
}
