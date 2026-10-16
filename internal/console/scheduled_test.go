package console

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestScheduledCommandReplaysAndReauthorizes(t *testing.T) {
	s, _, _ := serviceFixture(t)
	u := domain.User{ID: 1, Role: domain.RoleOperator}
	first, e := s.EnqueueCommandOnce(t.Context(), "schedule:command", "test", "list", u)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.EnqueueCommandOnce(t.Context(), "schedule:command", "test", "list", u)
	if e != nil || again.ID != first.ID {
		t.Fatal(first, again, e)
	}
	if _, e = s.EnqueueCommandOnce(t.Context(), "schedule:bad", "test", "stop", u); e == nil {
		t.Fatal("arbitrary command admitted")
	}
	if _, e = s.db.Exec(`UPDATE users SET role='viewer' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueCommandOnce(t.Context(), "schedule:command", "test", "list", u); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
}
