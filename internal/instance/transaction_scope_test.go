package instance

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestTransactionScopeRejectsCopiedExpiredAndOverlappingSession(t *testing.T) {
	s, _, _ := setup(t)
	if _, e := s.Create(t.Context(), admin, draft("alpha", 25565)); e != nil {
		t.Fatal(e)
	}
	var retained *StoppedSession
	e := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		retained = session
		copy := *session
		if e := copy.WithTransactionScope(t.Context(), func(context.Context, Detail, Directories) error { return nil }); domain.AsError(e).Code != domain.CodeForbidden {
			return errors.New("copy admitted")
		}
		return session.WithTransactionScope(t.Context(), func(ctx context.Context, d Detail, p Directories) error {
			if d.Instance.ID != "alpha" || p.Server == "" {
				return errors.New("wrong bound identity")
			}
			if _, e := session.Detail(ctx); domain.AsError(e).Code != domain.CodeInstanceBusy {
				return errors.New("overlap admitted")
			}
			return nil
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if e := retained.WithTransactionScope(context.Background(), func(context.Context, Detail, Directories) error { return nil }); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("expired admitted", e)
	}
}

func TestTransactionScopeDrainsBeforeStoppedAdmissionReturns(t *testing.T) {
	s, _, _ := setup(t)
	if _, e := s.Create(t.Context(), admin, draft("alpha", 25565)); e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- s.withStoppedSession(context.Background(), "alpha", func(session *StoppedSession) error {
			go func() {
				_ = session.WithTransactionScope(context.Background(), func(context.Context, Detail, Directories) error { close(entered); <-release; return nil })
			}()
			<-entered
			return nil
		})
	}()
	<-entered
	select {
	case e := <-returned:
		t.Fatal("stopped admission returned with live transaction", e)
	default:
	}
	close(release)
	if e := <-returned; e != nil {
		t.Fatal(e)
	}
}
