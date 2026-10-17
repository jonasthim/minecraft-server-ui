package api

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"net/http"
	"net/http/httptest"
	"os"
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

// The response writer advances a local clock to model slow output and honors
// actual SetWriteDeadline calls. No process-wide clock or long sleep is needed.
type timedJobStreamWriter struct {
	*httptest.ResponseRecorder
	now                     *time.Time
	deadlines               []time.Time
	deadline                time.Time
	attempts                int
	initialDelay, nextDelay time.Duration
	afterSnapshot           func()
	cancel                  context.CancelFunc
	denied                  bool
}

func (w *timedJobStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	w.deadlines = append(w.deadlines, deadline)
	return nil
}
func (w *timedJobStreamWriter) Write(value []byte) (int, error) {
	w.attempts++
	delay := w.nextDelay
	if w.attempts == 1 {
		delay = w.initialDelay
	}
	*w.now = w.now.Add(delay)
	if !w.deadline.IsZero() && !w.now.Before(w.deadline) {
		w.denied = true
		w.cancel()
		return 0, os.ErrDeadlineExceeded
	}
	return w.ResponseRecorder.Write(value)
}
func (w *timedJobStreamWriter) Flush() {
	w.ResponseRecorder.Flush()
	if w.attempts == 1 && w.afterSnapshot != nil {
		w.afterSnapshot()
		w.afterSnapshot = nil
	} else if w.attempts > 1 {
		w.cancel()
	}
}

func TestJobStreamAbsoluteExpiryBoundsSlowAndNearExpiryOutput(t *testing.T) {
	for _, test := range []struct {
		name                                   string
		initialDelay, afterSnapshot, nextDelay time.Duration
		heartbeat                              bool
		wantAttempts                           int
		wantDenied                             bool
	}{
		{name: "slow snapshot then near-expiry event", initialDelay: 4 * time.Second, afterSnapshot: 29 * time.Second, nextDelay: 2 * time.Second, wantAttempts: 2, wantDenied: true},
		{name: "event selected at expiry", afterSnapshot: 30 * time.Second, wantAttempts: 1},
		{name: "near-expiry heartbeat", afterSnapshot: 29 * time.Second, nextDelay: 2 * time.Second, heartbeat: true, wantAttempts: 2, wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, err := db.OpenMemory(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			// An inert durable queued job gives the stream a deterministic snapshot;
			// cancellation below publishes a real service progress event.
			_, err = database.Exec(`INSERT INTO jobs(id,kind,state,created_at,cancellable,requested_by) VALUES('stream-job','test','queued','2026-10-16T00:00:00Z',1,1)`)
			if err != nil {
				t.Fatal(err)
			}
			service := jobs.New(database, nil)
			defer service.Stop(context.Background())
			start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
			now := start
			req := userRequest("GET", "/jobs/stream-job/events", &domain.User{ID: 1, Role: domain.RoleViewer})
			ctx, cancel := context.WithCancel(req.Context())
			defer cancel()
			req = req.WithContext(ctx)
			writer := &timedJobStreamWriter{ResponseRecorder: httptest.NewRecorder(), now: &now, initialDelay: test.initialDelay, nextDelay: test.nextDelay, cancel: cancel}
			writer.afterSnapshot = func() {
				now = start.Add(test.afterSnapshot)
				if !test.heartbeat {
					if err := service.Cancel(context.Background(), "stream-job", domain.User{ID: 1, Role: domain.RoleOperator}); err != nil {
						t.Error(err)
						cancel()
					}
				}
			}
			heartbeat := 15 * time.Second
			if test.heartbeat {
				heartbeat = time.Nanosecond
			}
			router := chi.NewRouter()
			router.With(auth.RequireRole(domain.RoleViewer)).Get("/jobs/{jobId}/events", func(w http.ResponseWriter, r *http.Request) {
				streamJobWithTiming(w, r, service, jobStreamTiming{now: func() time.Time { return now }, lifetime: 30 * time.Second, heartbeat: heartbeat})
			})
			router.ServeHTTP(writer, req)
			if writer.attempts != test.wantAttempts || writer.denied != test.wantDenied {
				t.Fatalf("expired output was attempted or accepted: attempts=%d denied=%v, want %d/%v", writer.attempts, writer.denied, test.wantAttempts, test.wantDenied)
			}
			expiry := start.Add(30 * time.Second)
			for _, deadline := range writer.deadlines {
				if deadline.After(expiry) {
					t.Fatalf("write deadline %s extends authorization past %s", deadline, expiry)
				}
			}
			if writer.Code != 200 || !strings.Contains(writer.Body.String(), "event: progress") {
				t.Fatalf("initial snapshot missing: %d %s", writer.Code, writer.Body.String())
			}
		})
	}
}

func TestJobStreamRejectsQuotaBeforeSSEHeadersAndReleasesFailedLookup(t *testing.T) {
	s, h, id := jobAPI(t)
	user := &domain.User{ID: 1, Role: domain.RoleViewer}
	// Missing snapshots may never retain admission, including repeated requests.
	for i := 0; i < events.MaxSubscribers+1; i++ {
		r := httptest.NewRecorder()
		h.ServeHTTP(r, userRequest("GET", "/jobs/missing-job/events", user))
		if r.Code != http.StatusNotFound {
			t.Fatalf("lookup %d: %d %s", i, r.Code, r.Body.String())
		}
	}
	subs := make([]*events.Subscription, 0, events.MaxSubscribers)
	defer func() {
		for _, sub := range subs {
			sub.Close()
		}
	}()
	for i := 0; i < events.MaxSubscribers; i++ {
		sub, err := s.Subscribe(id)
		if err != nil {
			t.Fatalf("admission %d: %v", i, err)
		}
		subs = append(subs, sub)
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, userRequest("GET", "/jobs/"+id+"/events", user))
	if r.Code != http.StatusServiceUnavailable || strings.Contains(r.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(r.Body.String(), `"code":"unavailable"`) {
		t.Fatalf("saturation committed stream headers or lost typed error: %d %v %s", r.Code, r.Header(), r.Body.String())
	}
	subs[0].Close()
	// An already cancelled request still performs admission/cleanup without
	// needing real connection timers or waiting for the 30-second lifetime.
	for i := 0; i < 2; i++ {
		req := userRequest("GET", "/jobs/"+id+"/events", user)
		ctx, cancel := context.WithCancel(req.Context())
		cancel()
		r = httptest.NewRecorder()
		h.ServeHTTP(r, req.WithContext(ctx))
		if r.Code == http.StatusServiceUnavailable {
			t.Fatalf("client close leaked admission: %s", r.Body.String())
		}
	}
}

func TestJobStreamClientCloseAfterSnapshotReleasesQuota(t *testing.T) {
	s, _, id := jobAPI(t)
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	req := userRequest("GET", "/jobs/"+id+"/events", &domain.User{ID: 1, Role: domain.RoleViewer})
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	writer := &timedJobStreamWriter{ResponseRecorder: httptest.NewRecorder(), now: &start, cancel: cancel, afterSnapshot: cancel}
	router := chi.NewRouter()
	router.Get("/jobs/{jobId}/events", func(w http.ResponseWriter, r *http.Request) {
		streamJobWithTiming(w, r, s, jobStreamTiming{now: func() time.Time { return start }, lifetime: 30 * time.Second, heartbeat: time.Hour})
	})
	router.ServeHTTP(writer, req.WithContext(ctx))
	if !strings.Contains(writer.Body.String(), "event: progress") {
		t.Fatalf("no initial snapshot: %s", writer.Body.String())
	}
	var subs []*events.Subscription
	defer func() {
		for _, sub := range subs {
			sub.Close()
		}
	}()
	for i := 0; i < events.MaxSubscribers; i++ {
		sub, err := s.Subscribe(id)
		if err != nil {
			t.Fatalf("client close leaked quota at slot %d: %v", i, err)
		}
		subs = append(subs, sub)
	}
}
