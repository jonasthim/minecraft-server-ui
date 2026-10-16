package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) *Service {
	t.Helper()
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := New(database, nil)
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if err := s.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func receive(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
		return ""
	}
}
func waitState(t *testing.T, s *Service, id string, state domain.JobState) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, e := s.Get(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == state {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	j, _ := s.Get(context.Background(), id)
	t.Fatalf("job state %s, want %s", j.State, state)
	return Job{}
}
func seedInstance(t *testing.T, s *Service, id string) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES(?,?,'vanilla','1.21.1',21,1024,?)`, id, id, 25565+len(id))
	if err != nil {
		t.Fatal(err)
	}
}
func enqueue(t *testing.T, s *Service, instance string) Job {
	t.Helper()
	j, e := s.Enqueue(context.Background(), Request{InstanceID: instance, Kind: "test", Payload: json.RawMessage(`{"input":"value"}`), RequestedBy: 1, Cancellable: true})
	if e != nil {
		t.Fatal(e)
	}
	return j
}

// A cancelled worker still owns the instance until its cleanup actually returns.
func TestCancellationDoesNotReleaseInstanceEarly(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	started := make(chan string, 3)
	cleanup := make(chan struct{})
	defer close(cleanup)
	if e := s.Register("test", func(ctx context.Context, e *Execution) error {
		started <- e.Job.ID
		if e.Job.ID == "" {
			t.Error("missing id")
		}
		<-ctx.Done()
		<-cleanup
		return ctx.Err()
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Start(context.Background()); e != nil {
		t.Fatal(e)
	}
	one := enqueue(t, s, "a")
	if got := receive(t, started); got != one.ID {
		t.Fatal(got)
	}
	two := enqueue(t, s, "a")
	if e := s.Cancel(context.Background(), one.ID, domain.User{ID: 2, Role: domain.RoleOperator}); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatalf("other operator cancellation: %v", e)
	}
	if e := s.Cancel(context.Background(), one.ID, domain.User{ID: 1, Role: domain.RoleOperator}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
		t.Fatal("same instance ran before worker cleanup finished")
	case <-time.After(40 * time.Millisecond):
	}
	if j, _ := s.Get(context.Background(), two.ID); j.State != domain.JobQueued {
		t.Fatalf("second state %s", j.State)
	}
}

// Removing the global limit starts a fifth instance before a slot is available.
func TestGlobalConcurrencyAndQueuedCancellation(t *testing.T) {
	s := testService(t)
	started := make(chan string, 8)
	release := make(chan struct{})
	defer close(release)
	s.Register("test", func(ctx context.Context, e *Execution) error {
		started <- e.Job.ID
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})
	s.Start(context.Background())
	var all []Job
	for _, id := range []string{"a", "bb", "ccc", "dddd", "eeeee"} {
		seedInstance(t, s, id)
		all = append(all, enqueue(t, s, id))
	}
	for range 4 {
		receive(t, started)
	}
	select {
	case <-started:
		t.Fatal("fifth worker exceeded global limit")
	case <-time.After(40 * time.Millisecond):
	}
	if err := s.Cancel(context.Background(), all[4].ID, domain.User{ID: 1, Role: domain.RoleOperator}); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, all[4].ID, domain.JobCancelled)
}

// Restart must not falsely report partial side effects as success or replay them.
func TestRestartFailsInterruptedAndResumesQueuedPayload(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "manager.db")
	database, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO jobs(id,kind,state,phase,created_at,payload,requested_by,cancellable) VALUES('interrupted','test','running','activating','2026-10-16T00:00:00Z','{}',1,1),('waiting','test','queued','','2026-10-16T00:00:01Z','{"input":"persisted"}',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	database, err = db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := New(database, nil)
	defer s.Stop(ctx)
	got := make(chan string, 1)
	s.Register("test", func(ctx context.Context, e *Execution) error { got <- string(e.Payload); return nil })
	if err = s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if v := receive(t, got); v != `{"input":"persisted"}` {
		t.Fatalf("payload lost: %s", v)
	}
	j := waitState(t, s, "interrupted", domain.JobFailed)
	if j.Error == "" || j.Phase != "activating" {
		t.Fatalf("recovery lost journal: %+v", j)
	}
	waitState(t, s, "waiting", domain.JobSucceeded)
}

// Payload credentials, arbitrary upstream errors, and bearer values cannot escape.
func TestDurableProgressRedactsSecretsAndOpaqueFailures(t *testing.T) {
	s := testService(t)
	s.Register("test", func(ctx context.Context, e *Execution) error {
		if err := e.Phase(ctx, "download", 37); err != nil {
			return err
		}
		if err := e.Log(ctx, `token=abc123 password=hidden Bearer xyz987 private-value`); err != nil {
			return err
		}
		return errors.New("raw-upstream-private-error")
	})
	s.Start(context.Background())
	j, err := s.Enqueue(context.Background(), Request{Kind: "test", RequestedBy: 1, Payload: json.RawMessage(`{"client_secret":"private-value"}`), Cancellable: true})
	if err != nil {
		t.Fatal(err)
	}
	j = waitState(t, s, j.ID, domain.JobFailed)
	logs, err := s.Logs(context.Background(), j.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(logs)
	for _, v := range []string{"abc123", "hidden", "xyz987", "private-value", "raw-upstream-private-error"} {
		if strings.Contains(string(raw)+j.Error, v) {
			t.Fatalf("secret leaked: %s", v)
		}
	}
	if j.Progress != 37 || j.Phase != "download" {
		t.Fatalf("progress lost: %+v", j)
	}
	if len(logs) == 0 {
		t.Fatal("logs not durable")
	}
}

// Completion cannot lose its slot release or report cancelled work as success.
func TestCancellationWaitsThenNextQueuedJobRuns(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	started := make(chan string, 2)
	release := make(chan struct{})
	first := true
	s.Register("test", func(ctx context.Context, e *Execution) error {
		started <- e.Job.ID
		if first {
			first = false
			<-ctx.Done()
			cleanup, cancel := e.CleanupContext()
			defer cancel()
			if cleanup.Err() != nil {
				t.Error("cleanup context inherits cancellation")
			}
			<-release
			return nil
		}
		return nil
	})
	s.Start(context.Background())
	one := enqueue(t, s, "a")
	receive(t, started)
	two := enqueue(t, s, "a")
	if err := s.Cancel(context.Background(), one.ID, domain.User{ID: 1, Role: domain.RoleOperator}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := receive(t, started); got != two.ID {
		t.Fatalf("wrong next job %s", got)
	}
	waitState(t, s, one.ID, domain.JobCancelled)
	waitState(t, s, two.ID, domain.JobSucceeded)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='job.cancel' AND user_id=1 AND target=?`, one.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("cancellation audit missing: %d %v", count, err)
	}
}

// Shutdown must refuse new work immediately and must not hide stuck cleanup.
func TestShutdownRejectsEnqueueWaitsAndClosesSubscribers(t *testing.T) {
	s := testService(t)
	started := make(chan string, 1)
	release := make(chan struct{})
	s.Register("test", func(ctx context.Context, e *Execution) error {
		started <- e.Job.ID
		<-ctx.Done()
		<-release
		return ctx.Err()
	})
	s.Start(context.Background())
	j := enqueue(t, s, "")
	receive(t, started)
	sub := s.Subscribe(j.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := s.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown did not wait for worker: %v", err)
	}
	if _, err := s.Enqueue(context.Background(), Request{Kind: "test"}); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("enqueue during shutdown: %v", err)
	}
	for range sub.Events {
	}
	close(release)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, j.ID, domain.JobCancelled)
}

// User input may only select registered work and an existing safe instance ID.
func TestEnqueueValidationAndUnsafeCancellation(t *testing.T) {
	s := testService(t)
	s.Register("test", func(ctx context.Context, e *Execution) error { <-ctx.Done(); return ctx.Err() })
	s.Start(context.Background())
	for _, request := range []Request{{Kind: "unknown"}, {Kind: "test", InstanceID: "../escape"}, {Kind: "test", InstanceID: "missing"}, {Kind: "test", Payload: json.RawMessage(`{"bad"`)}, {Kind: "test", RequestedBy: -1}} {
		if _, err := s.Enqueue(context.Background(), request); err == nil {
			t.Fatalf("accepted invalid request %+v", request)
		}
	}
	j, err := s.Enqueue(context.Background(), Request{Kind: "test", RequestedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(context.Background(), j.ID, domain.User{ID: 1, Role: domain.RoleAdmin}); domain.AsError(err).Code != domain.CodeConflict {
		t.Fatalf("unsafe cancellation allowed: %v", err)
	}
}
