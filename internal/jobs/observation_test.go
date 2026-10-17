package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// The observer holds no global admission lock or SQL connection. Same-instance
// jobs are persisted immediately, defer dispatch, and block later observations.
func TestObservationDefersDurableWorkAndAllowsUnrelatedProgress(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	seedInstance(t, s, "bb")
	started := make(chan string, 2)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		if err := e.Phase(ctx, "observations-drained", 50); err != nil {
			return err
		}
		started <- e.Job.ID
		return nil
	})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- s.WithObservation(t.Context(), "a", func() error {
			var exists int
			if err := s.db.QueryRowContext(t.Context(), `SELECT 1 FROM instances WHERE id='a'`).Scan(&exists); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	awaitValue(t, entered)
	for name, guard := range map[string]func(context.Context, string, func() error) error{"idle": s.WithIdle, "observation": s.WithObservation} {
		if err := guard(t.Context(), "a", func() error { t.Error(name + " overlapped observation"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
			t.Fatalf("%s ignored observation: %v", name, err)
		}
	}
	target := enqueue(t, s, "a")
	s.dispatch() // Synchronous dispatch must leave the observed instance queued.
	waitState(t, s, target.ID, domain.JobQueued)
	var payload string
	if err := s.db.QueryRow(`SELECT payload FROM jobs WHERE id=?`, target.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `{"input":"value"}` {
		t.Fatalf("private input not persisted: %q", payload)
	}
	other := enqueue(t, s, "bb")
	if got := receive(t, started); got != other.ID {
		t.Fatalf("observation dispatched target early: %s", got)
	}
	waitState(t, s, other.ID, domain.JobSucceeded)
	once.Do(func() { close(release) })
	if err := awaitValue(t, done); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, started); got != target.ID {
		t.Fatalf("observation return did not wake target: %s", got)
	}
	waitState(t, s, target.ID, domain.JobSucceeded)
}

// Queued jobs get priority over new readers even when every slot is occupied.
func TestObservationRejectsQueuedAndRunningWork(t *testing.T) {
	s := testService(t)
	started := make(chan string, 4)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error { started <- e.Job.ID; <-ctx.Done(); return ctx.Err() })
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "bb", "ccc", "dddd", "eeeee"} {
		seedInstance(t, s, id)
		enqueue(t, s, id)
	}
	for range 4 {
		receive(t, started)
	}
	for _, id := range []string{"a", "eeeee"} {
		if err := s.WithObservation(t.Context(), id, func() error { t.Error("busy observation entered"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
			t.Fatalf("busy %s admitted: %v", id, err)
		}
	}
}

func TestObservationRejectsIdleAndPausedRecovery(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "idle", true: "recovery"}[paused], func(t *testing.T) {
			s := testService(t)
			var err error
			if paused {
				err = s.StartPaused(t.Context())
			} else {
				err = s.Start(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			guard := s.WithIdle
			if paused {
				guard = s.WithPausedInstance
			}
			if err := guard(t.Context(), "a", func() error {
				called := false
				err := s.WithObservation(t.Context(), "a", func() error { called = true; return nil })
				want := domain.CodeInstanceBusy
				if paused {
					want = domain.CodeUnavailable
				}
				if domain.AsError(err).Code != want || called {
					t.Fatalf("reservation overlapped: %v called=%v", err, called)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObservationValidatesAdmissionAndPreservesErrors(t *testing.T) {
	s := testService(t)
	callback := func() error { t.Error("invalid callback entered"); return nil }
	if err := s.WithObservation(t.Context(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal(err)
	}
	if err := s.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.WithObservation(t.Context(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal(err)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := s.WithObservation(nil, "a", callback); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "Uppercase", "has_underscore", "-leading"} {
		if err := s.WithObservation(t.Context(), id, callback); domain.AsError(err).Code != domain.CodeValidationFailed {
			t.Fatalf("id %q: %v", id, err)
		}
	}
	if err := s.WithObservation(t.Context(), "a", nil); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WithObservation(ctx, "a", callback); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	marker := errors.New("observation failed")
	if err := s.WithObservation(t.Context(), "a", func() error { return marker }); !errors.Is(err, marker) {
		t.Fatal(err)
	}
	if err := s.WithObservation(t.Context(), "a", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.WithObservation(t.Context(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal(err)
	}
}

// Cancel and panic must not free dispatch during deferred callback cleanup.
func TestObservationRetainsReservationThroughCleanup(t *testing.T) {
	for _, outcome := range []string{"cancel", "panic", "error"} {
		t.Run(outcome, func(t *testing.T) {
			s := testService(t)
			seedInstance(t, s, "a")
			started := make(chan string, 1)
			mustRegister(t, s, "test", func(_ context.Context, e *Execution) error { started <- e.Job.ID; return nil })
			if err := s.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			entered, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			marker := errors.New("observation result")
			type result struct {
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() {
				var r result
				defer func() { r.panicked = recover(); done <- r }()
				r.err = s.WithObservation(ctx, "a", func() error {
					close(entered)
					defer func() { close(cleaning); <-release }()
					if outcome == "panic" {
						panic(marker)
					}
					if outcome == "cancel" {
						<-ctx.Done()
						return ctx.Err()
					}
					return marker
				})
			}()
			awaitValue(t, entered)
			cancel()
			awaitValue(t, cleaning)
			target := enqueue(t, s, "a")
			s.dispatch()
			waitState(t, s, target.ID, domain.JobQueued)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer stopCancel()
			if err := s.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Stop ignored active cleanup: %v", err)
			}
			if err := s.WithObservation(t.Context(), "bb", func() error { t.Error("Stop admitted observer"); return nil }); domain.AsError(err).Code != domain.CodeUnavailable {
				t.Fatal(err)
			}
			once.Do(func() { close(release) })
			r := awaitValue(t, done)
			switch outcome {
			case "panic":
				if r.panicked != marker {
					t.Fatalf("panic changed: %v", r.panicked)
				}
			case "cancel":
				if !errors.Is(r.err, context.Canceled) {
					t.Fatal(r.err)
				}
			default:
				if !errors.Is(r.err, marker) {
					t.Fatal(r.err)
				}
			}
			if err := s.Stop(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitState(t, s, target.ID, domain.JobQueued)
			select {
			case id := <-started:
				t.Fatalf("Stop dispatched queued work: %s", id)
			default:
			}
		})
	}
}

func TestObservationPanicReleasesReservationAndWakesDispatch(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	started := make(chan string, 1)
	mustRegister(t, s, "test", func(_ context.Context, e *Execution) error { started <- e.Job.ID; return nil })
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		_ = s.WithObservation(t.Context(), "a", func() error { close(entered); <-release; panic("observer panic") })
	}()
	awaitValue(t, entered)
	target := enqueue(t, s, "a")
	once.Do(func() { close(release) })
	if got := awaitValue(t, done); got != "observer panic" {
		t.Fatal(got)
	}
	if got := receive(t, started); got != target.ID {
		t.Fatal(got)
	}
	waitState(t, s, target.ID, domain.JobSucceeded)
	if err := s.WithObservation(t.Context(), "a", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
