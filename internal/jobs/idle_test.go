package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Dropping the reservation allows a job to race instance files; holding the
// global mutex instead prevents unrelated jobs from reporting progress.
func TestWithIdleExcludesSameInstanceAndAllowsUnrelatedProgress(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	seedInstance(t, s, "bb")
	started := make(chan string, 2)
	if err := s.Register("test", func(ctx context.Context, e *Execution) error {
		if err := e.Phase(ctx, "during-mutation", 50); err != nil {
			return err
		}
		if err := e.Log(ctx, "Other instance continues"); err != nil {
			return err
		}
		started <- e.Job.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- s.WithIdle(context.Background(), "a", func() error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := s.db.ExecContext(ctx, `UPDATE instances SET name='mutated' WHERE id='a'`); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-idleDone:
		t.Fatalf("callback could not acquire sole DB connection: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("callback did not enter")
	}
	admission := make(chan error, 1)
	go func() {
		_, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "test"})
		admission <- err
	}()
	select {
	case err := <-admission:
		if domain.AsError(err).Code != domain.CodeInstanceBusy {
			t.Fatalf("reserved admission returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reservation held global admission mutex")
	}
	nestedCalled := false
	if err := s.WithIdle(context.Background(), "a", func() error { nestedCalled = true; return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy || nestedCalled {
		t.Fatalf("overlapping mutation accepted: %v", err)
	}
	other := enqueue(t, s, "bb")
	if got := receive(t, started); got != other.ID {
		t.Fatalf("wrong unrelated job %s", got)
	}
	waitState(t, s, other.ID, domain.JobSucceeded)
	dispatched := make(chan struct{})
	go func() { s.dispatch(); close(dispatched) }()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("callback blocked queue dispatch")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-idleDone; err != nil {
		t.Fatal(err)
	}
	next := enqueue(t, s, "a")
	if got := receive(t, started); got != next.ID {
		t.Fatalf("reservation was not released: %s", got)
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM instances WHERE id='a'`).Scan(&name); err != nil || name != "mutated" {
		t.Fatalf("mutation failed: %q %v", name, err)
	}
}

// A queued job is busy even if another instance holds all execution capacity.
func TestWithIdleRejectsQueuedAndRunningJobs(t *testing.T) {
	s := testService(t)
	started := make(chan string, 8)
	if err := s.Register("test", func(ctx context.Context, e *Execution) error { started <- e.Job.ID; <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
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
		called := false
		err := s.WithIdle(context.Background(), id, func() error { called = true; return nil })
		if domain.AsError(err).Code != domain.CodeInstanceBusy || called {
			t.Fatalf("busy %s accepted: called=%v err=%v", id, called, err)
		}
	}
	// Jobs in other instances do not prevent an unrelated idle mutation.
	if err := s.WithIdle(context.Background(), "new-instance", func() error { return nil }); err != nil {
		t.Fatalf("unrelated instance rejected: %v", err)
	}
}

// Validation/shutdown must prevent callbacks; failed or cancelled callbacks
// must release the lock so the next instance operation can proceed.
func TestWithIdleValidationErrorsAndCancellationReleaseLock(t *testing.T) {
	s := testService(t)
	called := false
	callback := func() error { called = true; return nil }
	if err := s.WithIdle(context.Background(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable || called {
		t.Fatalf("unstarted service accepted: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "Uppercase", "has_underscore", "-leading"} {
		called = false
		if err := s.WithIdle(context.Background(), id, callback); domain.AsError(err).Code != domain.CodeValidationFailed || called {
			t.Fatalf("invalid ID %q accepted: %v", id, err)
		}
	}
	marker := errors.New("mutation failed")
	if err := s.WithIdle(context.Background(), "a", func() error { return marker }); !errors.Is(err, marker) {
		t.Fatalf("callback error lost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called = false
	if err := s.WithIdle(ctx, "a", callback); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled caller admitted: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	if err := s.WithIdle(ctx, "a", func() error { cancel(); return ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation lost: %v", err)
	}
	if err := s.WithIdle(context.Background(), "a", func() error { return nil }); err != nil {
		t.Fatalf("error/cancellation retained admission lock: %v", err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := s.WithIdle(context.Background(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable || called {
		t.Fatalf("stopped service admitted callback: %v", err)
	}
}

// Cancellation cannot free a reservation while callback cleanup still runs.
func TestWithIdleCancellationRetainsReservationUntilReturn(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "a")
	if err := s.Register("test", func(context.Context, *Execution) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- s.WithIdle(ctx, "a", func() error { close(entered); <-ctx.Done(); <-release; return ctx.Err() })
	}()
	<-entered
	cancel()
	if _, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "test"}); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("cancellation freed active callback: %v", err)
	}
	once.Do(func() { close(release) })
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("callback cancellation lost: %v", err)
	}
	if _, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "test"}); err != nil {
		t.Fatalf("returned callback retained reservation: %v", err)
	}
}

func TestWithIdlePanicReleasesReservation(t *testing.T) {
	s := testService(t)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected callback panic")
			}
		}()
		_ = s.WithIdle(context.Background(), "a", func() error { panic("mutation panic") })
	}()
	if err := s.WithIdle(context.Background(), "a", func() error { return nil }); err != nil {
		t.Fatalf("panic retained reservation: %v", err)
	}
}

// Shutdown cannot declare cleanup drained while a file/DB mutation is active.
func TestWithIdleParticipatesInShutdownDrain(t *testing.T) {
	s := testService(t)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- s.WithIdle(context.Background(), "a", func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown ignored callback cleanup: %v", err)
	}
	called := false
	if err := s.WithIdle(context.Background(), "bb", func() error { called = true; return nil }); domain.AsError(err).Code != domain.CodeUnavailable || called {
		t.Fatalf("shutdown admitted new mutation: %v", err)
	}
	once.Do(func() { close(release) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
