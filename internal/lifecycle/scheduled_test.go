package lifecycle

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"testing"
)

func TestRecoveryStartRejectsForgedExecutionBeforeNativeIO(t *testing.T) {
	s, user, native := fixture(t)
	guard := func(ctx context.Context, id string, start func() (domain.ProcessIdentity, error)) (domain.ProcessIdentity, error) {
		t.Fatal("forged execution reached guard")
		return start()
	}
	for _, e := range []*jobs.Execution{nil, {Job: domain.Job{InstanceID: "test", Kind: "crash_recovery"}}} {
		if err := s.StartForRecovery(t.Context(), e, user, guard); err == nil || native.starts != 0 {
			t.Fatal(native.starts, err)
		}
	}
}
func TestScheduledRestartIdempotencyAndCurrentActor(t *testing.T) {
	s, user, _ := fixture(t)
	first, e := s.EnqueueRestartOnce(t.Context(), "schedule:restart", "test", 300, user)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.EnqueueRestartOnce(t.Context(), "schedule:restart", "test", 300, user)
	if e != nil || first.ID != again.ID {
		t.Fatal(first, again, e)
	}
	if _, e = s.db.Exec(`INSERT INTO users(username,role,created_at,updated_at) VALUES('reserve-admin','admin','now','now')`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`UPDATE users SET disabled=1 WHERE id=?`, user.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueRestartOnce(t.Context(), "schedule:second", "test", 300, user); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
}
func TestDeliberateStopHookRunsBeforeJobAdmission(t *testing.T) {
	s, user, _ := fixture(t)
	called := false
	s.stopIntent = func(context.Context, string, domain.User) error {
		called = true
		return domain.E(domain.CodeUnavailable, "stop fence unavailable")
	}
	if _, e := s.Enqueue(t.Context(), user, "test", Request{Operation: "stop"}); e == nil || !called {
		t.Fatal(called, e)
	}
	var count int
	if e := s.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind='lifecycle'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}
}
