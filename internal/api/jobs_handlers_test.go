package api

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jobAPI(t *testing.T) (*jobs.Service, http.Handler, string) {
	t.Helper()
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := jobs.New(database, nil)
	s.Register("test", func(ctx context.Context, e *jobs.Execution) error {
		e.Phase(ctx, "waiting", 12)
		e.Log(ctx, "Safe test output")
		<-ctx.Done()
		return ctx.Err()
	})
	if err = s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	j, err := s.Enqueue(context.Background(), jobs.Request{Kind: "test", RequestedBy: 1, Cancellable: true, Payload: json.RawMessage(`{"password":"never-expose-me"}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(context.Background()); database.Close() })
	r := chi.NewRouter()
	MountJobs(r, s)
	return s, r, j.ID
}
func userRequest(method, path string, user *domain.User) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if user != nil {
		r = r.WithContext(auth.WithUser(r.Context(), user))
	}
	return r
}

// Gates must cover streams and log downloads as well as JSON reads/mutations.
func TestJobsRoleMatrixAndPayloadPrivacy(t *testing.T) {
	_, h, id := jobAPI(t)
	for _, tail := range []string{"", "/logs", "/logs/download", "/events"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, userRequest("GET", "/jobs/"+id+tail, nil))
		if r.Code != 401 {
			t.Fatalf("anonymous %s = %d", tail, r.Code)
		}
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		u := &domain.User{ID: 2, Role: role}
		r := httptest.NewRecorder()
		h.ServeHTTP(r, userRequest("GET", "/jobs/"+id, u))
		if r.Code != 200 || strings.Contains(r.Body.String(), "never-expose-me") || strings.Contains(r.Body.String(), "payload") {
			t.Fatalf("read %s: %d %s", role, r.Code, r.Body.String())
		}
		r = httptest.NewRecorder()
		h.ServeHTTP(r, userRequest("POST", "/jobs/"+id+"/cancel", u))
		want := 403
		if role == domain.RoleAdmin {
			want = 200
		}
		if r.Code != want {
			t.Fatalf("cancel %s: %d %s", role, r.Code, r.Body.String())
		}
	}
}

// A live authorized stream delivers a snapshot and exits on manager shutdown.
func TestJobEventStreamSnapshotAndShutdown(t *testing.T) {
	s, h, id := jobAPI(t)
	r := httptest.NewRecorder()
	req := userRequest("GET", "/jobs/"+id+"/events", &domain.User{ID: 1, Role: domain.RoleViewer})
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(r, req) }()
	time.Sleep(20 * time.Millisecond)
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE did not exit at shutdown")
	}
	if r.Code != 200 || !strings.Contains(r.Result().Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(r.Body.String(), "event: progress") || strings.Contains(r.Body.String(), "never-expose-me") {
		t.Fatalf("invalid stream %d %s", r.Code, r.Body.String())
	}
}
func TestJobLogsRejectInvalidCursor(t *testing.T) {
	_, h, id := jobAPI(t)
	for _, q := range []string{"after=-1", "after=oops", "limit=501", "limit=0"} {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, userRequest("GET", "/jobs/"+id+"/logs?"+q, &domain.User{ID: 1, Role: domain.RoleViewer}))
		if r.Code != 422 {
			t.Fatalf("%s returned %d", q, r.Code)
		}
	}
}
