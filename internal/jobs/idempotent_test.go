package jobs

import (
	"context"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestIdempotentAdmissionReplayAndAtomicity(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "alpha")
	if e := s.Register("test", func(_ctx context.Context, _e *Execution) error { return nil }); e != nil {
		t.Fatal(e)
	}
	if e := s.StartPaused(t.Context()); e != nil {
		t.Fatal(e)
	}
	request := Request{InstanceID: "alpha", Kind: "test", Payload: json.RawMessage(`{"b":2,"a":1}`), RequestedBy: 1, Cancellable: true}
	first, e := s.EnqueueIdempotent(t.Context(), "schedule:one", request)
	if e != nil {
		t.Fatal(e)
	}
	request.Payload = json.RawMessage(`{ "a":1, "b":2 }`)
	again, e := s.EnqueueIdempotent(t.Context(), "schedule:one", request)
	if e != nil || again.ID != first.ID {
		t.Fatal(again, e)
	}
	if _, e = s.db.Exec(`UPDATE jobs SET state='succeeded' WHERE id=?`, first.ID); e != nil {
		t.Fatal(e)
	}
	again, e = s.EnqueueIdempotent(t.Context(), "schedule:one", request)
	if e != nil || again.ID != first.ID || again.State != domain.JobSucceeded {
		t.Fatal(again, e)
	}
	request.RequestedBy = 2
	if _, e = s.EnqueueIdempotent(t.Context(), "schedule:one", request); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`CREATE TRIGGER deny_admission BEFORE INSERT ON job_admissions BEGIN SELECT RAISE(ABORT,'injected'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.EnqueueIdempotent(t.Context(), "schedule:two", request); e == nil {
		t.Fatal("missing atomic admission failure")
	}
	var n int
	if e = s.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if _, e = s.db.Exec(`DROP TRIGGER deny_admission`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.db.Exec(`DELETE FROM jobs WHERE id=?`, first.ID); e != nil {
		t.Fatal(e)
	}
	request.RequestedBy = 1
	if _, e = s.EnqueueIdempotent(t.Context(), "schedule:one", request); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("retention reused admission", e)
	}
}
