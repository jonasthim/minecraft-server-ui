package notify

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"strings"
	"testing"
)

func TestServerCrashIntentFiniteAndDeduplicated(t *testing.T) {
	s, now, actor := fixture(t)
	id := "crash:" + strings.Repeat("a", 64)
	if e := s.EnqueueServerCrash(t.Context(), id, "alpha", *now); e != nil {
		t.Fatal(e)
	}
	if e := s.EnqueueServerCrash(t.Context(), id, "alpha", *now); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := s.cfg.DB.QueryRow(`SELECT count(*) FROM notification_events WHERE kind='server_crashed' AND operation='crash_recovery'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	for _, event := range []Event{{ID: "x", InstanceID: "alpha", CreatedAt: *now, Kind: ServerCrashed, Operation: "backup_create"}, {ID: "x", InstanceID: "alpha", CreatedAt: *now, Kind: JobSucceeded, Operation: "crash_recovery"}} {
		if e := s.Enqueue(t.Context(), event); domain.AsError(e).Code != domain.CodeValidationFailed {
			t.Fatal(event, e)
		}
	}
	if e := s.EnqueueServerCrash(t.Context(), "crash:raw-secret", "alpha", *now); e == nil {
		t.Fatal("accepted unbounded native-derived content")
	}
	in := input("https://example.org/hook")
	in.Events = []EventKind{JobFailed, JobSucceeded, ServerCrashed}
	if _, e := s.Save(t.Context(), "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	in.Events = []EventKind{ServerCrashed, ServerCrashed}
	if _, e := s.Save(t.Context(), "ops", in, actor); e == nil {
		t.Fatal("accepted duplicate crash choice")
	}
}
