package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func consoleAPIFixture(t *testing.T) (*console.Service, *chi.Mux) {
	t.Helper()
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instances := instance.New(database, paths, instanceTestGuard{})
	_, err = instances.Create(t.Context(), domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(database, nil)
	s := console.New(database, paths, nil, instances, queue)
	if err = s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { queue.Stop(context.Background()) })
	r := chi.NewRouter()
	MountConsole(r, s)
	return s, r
}
func TestConsoleHTTPRoleAndInputBoundaries(t *testing.T) {
	_, router := consoleAPIFixture(t)
	request := func(method, path, body string, user *domain.User, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != nil {
			r = r.WithContext(auth.WithUser(r.Context(), user))
		}
		if token {
			r = r.WithContext(auth.WithTokenAuth(r.Context()))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/instances/alpha/console/logs", "/instances/alpha/console/events", "/instances/alpha/console/status"} {
		if w := request("GET", path, "", nil, false); w.Code != 401 {
			t.Fatalf("anonymous %s: %d", path, w.Code)
		}
	}
	viewer := &domain.User{ID: 1, Role: domain.RoleViewer}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"list"}`, viewer, false); w.Code != 403 {
		t.Fatal("viewer command", w.Code)
	}
	if w := request("GET", "/instances/alpha/console/logs?after=-1", "", viewer, false); w.Code != 422 {
		t.Fatal("cursor", w.Code)
	}
	if w := request("GET", "/instances/alpha/console/logs?after=1&after=2", "", viewer, false); w.Code != 422 {
		t.Fatal("duplicate cursor", w.Code)
	}
	if w := request("GET", "/instances/alpha/console/logs", "", viewer, false); w.Code != 200 || !strings.Contains(w.Body.String(), `"lines":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	operator := &domain.User{ID: 1, Role: domain.RoleOperator}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"stop"}`, operator, false); w.Code != 422 {
		t.Fatal("unsafe command", w.Code)
	}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"seed"}`, operator, false); w.Code != 403 {
		t.Fatal("seed role", w.Code)
	}
	admin := &domain.User{ID: 1, Role: domain.RoleAdmin}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"seed"}`, admin, true); w.Code != 403 {
		t.Fatal("seed missing token scope", w.Code)
	}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"list","extra":true}`, operator, false); w.Code != 422 {
		t.Fatal("unknown input", w.Code)
	}
	if w := request("POST", "/instances/alpha/console/commands", `{"command":"say Hello"}`, operator, false); w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestConsoleStreamAbsoluteExpiryAndQuota(t *testing.T) {
	s, _ := consoleAPIFixture(t)
	releases := []func(){}
	for i := 0; i < 64; i++ {
		release, err := s.AcquireStream(t.Context(), "alpha")
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, err := s.AcquireStream(t.Context(), "alpha"); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("quota: %v", err)
	}
	for _, release := range releases {
		release()
		release()
	}
	// Deadline starts before the snapshot and each write checks that boundary.
	now := time.Now()
	timing := consoleStreamTiming{now: func() time.Time { return now }, lifetime: time.Second, poll: time.Millisecond}
	r := httptest.NewRequest("GET", "/instances/alpha/console/events", nil)
	r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: domain.RoleViewer}))
	router := chi.NewRouter()
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/console/events", func(w http.ResponseWriter, r *http.Request) {
		now = now.Add(2 * time.Second)
		streamConsoleWithTiming(w, r, s, timing)
	})
	// Advance from inside a flush to emulate a slow write spanning absolute expiry.
	writer := &consoleDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), now: &now, delay: 2 * time.Second}
	router.ServeHTTP(writer, r)
	if writer.writes > 1 {
		t.Fatalf("stream wrote after absolute expiry: %d", writer.writes)
	}
}

type consoleDeadlineWriter struct {
	*httptest.ResponseRecorder
	now      *time.Time
	delay    time.Duration
	writes   int
	deadline time.Time
}

func (w *consoleDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}
func (w *consoleDeadlineWriter) Write(data []byte) (int, error) {
	if !w.deadline.IsZero() && w.now.After(w.deadline) {
		return 0, context.DeadlineExceeded
	}
	w.writes++
	return w.ResponseRecorder.Write(data)
}
func (w *consoleDeadlineWriter) Flush() { *w.now = w.now.Add(w.delay); w.ResponseRecorder.Flush() }
