package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not complete")
		var zero T
		return zero
	}
}
func mustRegister(t *testing.T, s *Service, kind string, h Handler) {
	t.Helper()
	if err := s.Register(kind, h); err != nil {
		t.Fatal(err)
	}
}

func TestStartPausedRecoversAndResumesExactSerialQueue(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	_, err := s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,phase,created_at,payload) VALUES('old-running',NULL,'test','running','activate','2026-10-16T00:00:00Z','{}'),('first','a','test','queued','','2026-10-16T00:00:01Z','{"sequence":1}'),('second','a','test','queued','','2026-10-16T00:00:02Z','{"sequence":2}')`)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 3)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		started <- string(e.Payload)
		if string(e.Payload) == `{"sequence":1}` {
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	if err := s.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Register("late", func(context.Context, *Execution) error { return nil }); err == nil {
		t.Fatal("paused startup did not freeze handlers")
	}
	recovered := waitState(t, s, "old-running", domain.JobFailed)
	if recovered.Phase != "activate" || recovered.Error == "" {
		t.Fatalf("recovery lost interruption journal: %+v", recovered)
	}
	third, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "test", Payload: json.RawMessage(`{"sequence":3}`)})
	if err != nil {
		t.Fatal(err)
	}
	s.dispatch()
	select {
	case value := <-started:
		t.Fatalf("paused dispatcher started %s", value)
	case <-time.After(20 * time.Millisecond):
	}
	if err := s.WithIdle(context.Background(), "new-instance", func() error { return nil }); err != nil {
		t.Fatalf("paused idle mutation unavailable: %v", err)
	}
	if err := s.WithIdle(context.Background(), "a", func() error { t.Error("queued instance admitted"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("queued idle guard: %v", err)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(); err != nil {
		t.Fatalf("repeat Resume must be idempotent: %v", err)
	}
	if value := receive(t, started); value != `{"sequence":1}` {
		t.Fatalf("first payload changed: %s", value)
	}
	s.dispatch()
	if j, _ := s.Get(context.Background(), "second"); j.State != domain.JobQueued {
		t.Fatalf("same-instance queue ran concurrently: %s", j.State)
	}
	once.Do(func() { close(release) })
	if value := receive(t, started); value != `{"sequence":2}` {
		t.Fatalf("second payload/order changed: %s", value)
	}
	if value := receive(t, started); value != `{"sequence":3}` {
		t.Fatalf("durable paused enqueue changed: %s", value)
	}
	waitState(t, s, third.ID, domain.JobSucceeded)
}

func TestPausedStopAndCancelledResumeNeverDispatch(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled", true: "stopped"}[stop], func(t *testing.T) {
			s := testService(t)
			started := make(chan string, 1)
			mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error { started <- e.Job.ID; return nil })
			if err := s.Resume(); domain.AsError(err).Code != domain.CodeUnavailable {
				t.Fatalf("unstarted Resume: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := s.StartPaused(ctx); err != nil {
				t.Fatal(err)
			}
			j := enqueue(t, s, "")
			if stop {
				if err := s.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			if err := s.Resume(); domain.AsError(err).Code != domain.CodeUnavailable {
				t.Fatalf("unavailable Resume accepted: %v", err)
			}
			if err := s.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
				t.Fatal("cancelled/stopped paused service dispatched")
			default:
			}
			waitState(t, s, j.ID, domain.JobQueued)
		})
	}
}

func TestExecutionMutationUsesPrivateIdentityAndRejectsCopies(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	ready := make(chan *Execution, 1)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		ready <- e
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	job := enqueue(t, s, "a")
	execution := awaitValue(t, ready)
	copied := *execution
	for _, candidate := range []*Execution{nil, {}, {Job: execution.Job}, {Job: execution.Job, service: s}, &copied} {
		called := false
		err := candidate.WithInstanceMutation(context.Background(), func(string) error { called = true; return nil })
		if domain.AsError(err).Code != domain.CodeForbidden || called {
			t.Fatalf("forged/copied execution admitted: called=%v err=%v", called, err)
		}
	}
	if err := execution.WithInstanceMutation(context.Background(), nil); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatalf("nil mutation callback accepted: %v", err)
	}
	execution.Job.ID = "another-job"
	execution.Job.InstanceID = "another-instance"
	if err := execution.WithInstanceMutation(context.Background(), func(id string) error {
		if id != "a" {
			t.Fatalf("public metadata changed authority: %s", id)
		}
		// Prove the callback holds neither the global queue mutex nor a SQL connection.
		if err := execution.Phase(context.Background(), "activate", 60); err != nil {
			return err
		}
		if err := execution.Log(context.Background(), "Safe activation phase"); err != nil {
			return err
		}
		_, err := s.db.Exec(`UPDATE instances SET name='installed' WHERE id=?`, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	actual, err := s.Get(context.Background(), job.ID)
	if err != nil || actual.Phase != "activate" {
		t.Fatalf("private job journal failed: %+v %v", actual, err)
	}
	if err := s.WithIdle(context.Background(), "a", func() error { t.Error("running instance admitted idle mutation"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("normal idle guard bypassed running job: %v", err)
	}
	once.Do(func() { close(release) })
	waitState(t, s, job.ID, domain.JobSucceeded)
	if err := execution.WithInstanceMutation(context.Background(), func(string) error { t.Error("stale execution admitted"); return nil }); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatalf("stale identity accepted: %v", err)
	}
}

func TestExecutionMutationRejectsInstanceLessJobs(t *testing.T) {
	s := testService(t)
	out := make(chan error, 1)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		out <- e.WithInstanceMutation(ctx, func(string) error { t.Error("instance-less mutation admitted"); return nil })
		return nil
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "")
	if err := awaitValue(t, out); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatalf("instance-less execution: %v", err)
	}
}

func TestExecutionMutationOverlapErrorPanicAndUnrelatedProgress(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	seedInstance(t, s, "bb")
	ready := make(chan *Execution, 1)
	finishHandler := make(chan struct{})
	var handlerOnce sync.Once
	defer handlerOnce.Do(func() { close(finishHandler) })
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		ready <- e
		select {
		case <-finishHandler:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	mustRegister(t, s, "other", func(ctx context.Context, e *Execution) error {
		if err := e.Phase(ctx, "other-progress", 50); err != nil {
			return err
		}
		return e.Log(ctx, "Other instance is working")
	})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	enqueue(t, s, "a")
	execution := awaitValue(t, ready)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	result := make(chan error, 1)
	marker := errors.New("callback failed")
	go func() {
		result <- execution.WithInstanceMutation(context.Background(), func(string) error { close(entered); <-release; return marker })
	}()
	<-entered
	called := false
	if err := execution.WithInstanceMutation(context.Background(), func(string) error { called = true; return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy || called {
		t.Fatalf("overlap admitted: %v", err)
	}
	other, err := s.Enqueue(context.Background(), Request{InstanceID: "bb", Kind: "other"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, s, other.ID, domain.JobSucceeded)
	once.Do(func() { close(release) })
	if err := awaitValue(t, result); !errors.Is(err, marker) {
		t.Fatalf("callback error lost: %v", err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected mutation panic")
			}
		}()
		_ = execution.WithInstanceMutation(context.Background(), func(string) error { panic("mutation panic") })
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := execution.WithInstanceMutation(ctx, func(string) error { t.Error("cancelled callback admitted"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled mutation: %v", err)
	}
	if err := execution.WithInstanceMutation(context.Background(), func(string) error { return nil }); err != nil {
		t.Fatalf("callback error/panic retained ownership: %v", err)
	}
	handlerOnce.Do(func() { close(finishHandler) })
}

func TestDetachedMutationDrainsBeforeSlotReleaseAndShutdown(t *testing.T) {
	for _, scenario := range []struct {
		name                      string
		shutdown, callbackFailure bool
	}{
		{name: "next-job"}, {name: "shutdown", shutdown: true}, {name: "failed-detached-callback", callbackFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			shutdown := scenario.shutdown
			var callbackError error
			if scenario.callbackFailure {
				callbackError = errors.New("detached activation failed")
			}
			s := testService(t)
			seedInstance(t, s, "a")
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			returned := make(chan *Execution, 1)
			mutationResult := make(chan error, 1)
			nextStarted := make(chan string, 1)
			mustRegister(t, s, "hold", func(ctx context.Context, e *Execution) error {
				go func() {
					mutationResult <- e.WithInstanceMutation(ctx, func(string) error { close(entered); <-release; return callbackError })
				}()
				<-entered
				returned <- e
				return nil
			})
			mustRegister(t, s, "next", func(ctx context.Context, e *Execution) error { nextStarted <- e.Job.ID; return nil })
			if err := s.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			first, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "hold"})
			if err != nil {
				t.Fatal(err)
			}
			execution := awaitValue(t, returned)
			// Wait for the handler-return admission boundary without relying on sleeps
			// as evidence that a detached call is late.
			deadline := time.Now().Add(3 * time.Second)
			for {
				called := false
				err := execution.WithInstanceMutation(context.Background(), func(string) error { called = true; return nil })
				if called {
					t.Fatal("detached callback overlapped active mutation")
				}
				if domain.AsError(err).Code == domain.CodeForbidden {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("handler return did not close mutation admission: %v", err)
				}
				time.Sleep(time.Millisecond)
			}
			next, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "next"})
			if err != nil {
				t.Fatal(err)
			}
			s.dispatch()
			if j, _ := s.Get(context.Background(), first.ID); j.State != domain.JobRunning {
				t.Fatalf("handler return freed live mutation: %s", j.State)
			}
			if j, _ := s.Get(context.Background(), next.ID); j.State != domain.JobQueued {
				t.Fatalf("next job bypassed detached mutation: %s", j.State)
			}
			if shutdown {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
				defer cancel()
				if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("shutdown ignored detached mutation: %v", err)
				}
			}
			once.Do(func() { close(release) })
			if err := awaitValue(t, mutationResult); !errors.Is(err, callbackError) {
				t.Fatalf("detached callback result changed: %v", err)
			}
			if shutdown {
				if err := s.Stop(context.Background()); err != nil {
					t.Fatal(err)
				}
				waitState(t, s, first.ID, domain.JobCancelled)
				waitState(t, s, next.ID, domain.JobQueued)
			} else {
				if id := receive(t, nextStarted); id != next.ID {
					t.Fatalf("wrong next job %s", id)
				}
				firstState := domain.JobSucceeded
				if scenario.callbackFailure {
					firstState = domain.JobFailed
				}
				waitState(t, s, first.ID, firstState)
				waitState(t, s, next.ID, domain.JobSucceeded)
			}
		})
	}
}
