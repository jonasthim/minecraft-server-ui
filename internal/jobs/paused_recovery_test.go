package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Rejecting queued jobs here prevents manager startup forever after restart;
// unpausing during recovery lets that durable work race filesystem cleanup.
func TestPausedInstanceRecoveryPreservesDurableQueueAndAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "manager.db")
	database, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s := New(database, nil)
	t.Cleanup(func() {
		stop, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := s.Stop(stop); err != nil {
			t.Error(err)
		}
		database.Close()
	})
	seedInstance(t, s, "a")
	seedInstance(t, s, "bb")
	mustRegister(t, s, "test", func(context.Context, *Execution) error {
		t.Error("initial paused queue dispatched")
		return nil
	})
	if err := s.StartPaused(ctx); err != nil {
		t.Fatal(err)
	}
	retained := enqueue(t, s, "a")
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := db.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	database = reopened
	s = New(database, nil)
	recoverInstance := s.WithPausedInstance
	started := make(chan string, 2)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		if string(e.Payload) != `{"input":"value"}` {
			t.Errorf("durable payload changed: %s", e.Payload)
		}
		started <- e.Job.ID
		return nil
	})
	if err := s.StartPaused(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- recoverInstance(ctx, "a", func() error {
			ioCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			// The SQLite pool has one connection. A retained connection or queue
			// mutex would prevent this update or unrelated admission below.
			if _, err := database.ExecContext(ioCtx, `UPDATE instances SET name='recovered' WHERE id='a'`); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("recovery did not enter with retained queued work: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("recovery blocked on queue mutex or SQL connection")
	}
	admission := make(chan error, 1)
	go func() {
		_, err := s.Enqueue(ctx, Request{InstanceID: "a", Kind: "test"})
		admission <- err
	}()
	if err := awaitValue(t, admission); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("recovery did not reserve target admission: %v", err)
	}
	for name, guard := range map[string]func(context.Context, string, func() error) error{
		"idle": s.WithIdle, "recovery": recoverInstance,
	} {
		called := false
		if err := guard(ctx, "a", func() error { called = true; return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy || called {
			t.Fatalf("%s overlapped recovery: called=%v err=%v", name, called, err)
		}
	}
	other := enqueue(t, s, "bb")
	if err := s.WithIdle(ctx, "unrelated", func() error { return nil }); err != nil {
		t.Fatalf("recovery blocked unrelated idle mutation: %v", err)
	}
	if err := s.Resume(); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("Resume admitted active recovery: %v", err)
	}
	s.dispatch()
	waitState(t, s, retained.ID, domain.JobQueued)
	waitState(t, s, other.ID, domain.JobQueued)
	once.Do(func() { close(release) })
	if err := awaitValue(t, done); err != nil {
		t.Fatal(err)
	}
	// Recovery return does not implicitly enable dispatch.
	s.dispatch()
	waitState(t, s, retained.ID, domain.JobQueued)
	if err := s.WithIdle(ctx, "a", func() error { t.Error("ordinary idle guard admitted queued work"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("ordinary idle semantics weakened: %v", err)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(); err != nil {
		t.Fatalf("successful Resume lost idempotency: %v", err)
	}
	seen := map[string]bool{receive(t, started): true, receive(t, started): true}
	if !seen[retained.ID] || !seen[other.ID] {
		t.Fatalf("retained/unrelated jobs did not execute: %v", seen)
	}
	waitState(t, s, retained.ID, domain.JobSucceeded)
	waitState(t, s, other.ID, domain.JobSucceeded)
	var name string
	if err := database.QueryRow(`SELECT name FROM instances WHERE id='a'`).Scan(&name); err != nil || name != "recovered" {
		t.Fatalf("recovery mutation lost: name=%q err=%v", name, err)
	}
}

// A single recovery boolean would allow Resume after only one of two distinct
// reservations returns; idle reservations must also exclude recovery admission.
func TestPausedInstanceRecoveryExcludesIdleAndResumeWaitsForEveryCallback(t *testing.T) {
	s := testService(t)
	recoverInstance := s.WithPausedInstance
	if err := s.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	idleDone := make(chan error, 1)
	go func() {
		idleDone <- s.WithIdle(context.Background(), "a", func() error { entered <- struct{}{}; <-release; return nil })
	}()
	awaitValue(t, entered)
	if err := recoverInstance(context.Background(), "a", func() error { t.Error("recovery overlapped idle callback"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("idle reservation ignored: %v", err)
	}
	once.Do(func() { close(release) })
	if err := awaitValue(t, idleDone); err != nil {
		t.Fatal(err)
	}
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var firstOnce, secondOnce sync.Once
	defer firstOnce.Do(func() { close(firstRelease) })
	defer secondOnce.Do(func() { close(secondRelease) })
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	go func() {
		firstDone <- recoverInstance(context.Background(), "a", func() error { entered <- struct{}{}; <-firstRelease; return nil })
	}()
	go func() {
		secondDone <- recoverInstance(context.Background(), "bb", func() error { entered <- struct{}{}; <-secondRelease; return nil })
	}()
	awaitValue(t, entered)
	awaitValue(t, entered)
	firstOnce.Do(func() { close(firstRelease) })
	if err := awaitValue(t, firstDone); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("Resume ignored second recovery: %v", err)
	}
	secondOnce.Do(func() { close(secondRelease) })
	if err := awaitValue(t, secondDone); err != nil {
		t.Fatal(err)
	}
	if err := s.Resume(); err != nil {
		t.Fatalf("drained recovery prevented Resume: %v", err)
	}
}

// Caller cancellation and panic unwinding must retain the reservation until
// deferred filesystem cleanup returns, while preserving the callback result.
func TestPausedInstanceRecoveryRetainsReservationThroughCleanup(t *testing.T) {
	for _, outcome := range []string{"error", "cancelled", "panic"} {
		t.Run(outcome, func(t *testing.T) {
			s := testService(t)
			recoverInstance := s.WithPausedInstance
			seedInstance(t, s, "a")
			mustRegister(t, s, "test", func(context.Context, *Execution) error { return nil })
			if err := s.StartPaused(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			marker := errors.New("recovery failed")
			type result struct {
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() {
				var got result
				defer func() { got.panicked = recover(); done <- got }()
				got.err = recoverInstance(ctx, "a", func() error {
					close(entered)
					defer func() { close(cleaning); <-release }()
					if outcome == "panic" {
						panic(marker)
					}
					if outcome == "cancelled" {
						<-ctx.Done()
						return ctx.Err()
					}
					return marker
				})
			}()
			if outcome == "cancelled" {
				// Wait for callback entry before cancelling, otherwise admission
				// correctly rejects it without invoking cleanup.
				awaitValue(t, entered)
				cancel()
			}
			awaitValue(t, cleaning)
			if _, err := s.Enqueue(context.Background(), Request{InstanceID: "a", Kind: "test"}); domain.AsError(err).Code != domain.CodeInstanceBusy {
				t.Fatalf("cleanup released target admission: %v", err)
			}
			if err := s.Resume(); domain.AsError(err).Code != domain.CodeInstanceBusy {
				t.Fatalf("cleanup released resume gate: %v", err)
			}
			once.Do(func() { close(release) })
			got := awaitValue(t, done)
			switch outcome {
			case "panic":
				if got.panicked != marker {
					t.Fatalf("callback panic changed: %v", got.panicked)
				}
			case "cancelled":
				if !errors.Is(got.err, context.Canceled) {
					t.Fatalf("callback cancellation changed: %v", got.err)
				}
			default:
				if !errors.Is(got.err, marker) {
					t.Fatalf("callback error changed: %v", got.err)
				}
			}
			if err := recoverInstance(context.Background(), "a", func() error { return nil }); err != nil {
				t.Fatalf("returned callback retained reservation: %v", err)
			}
			if err := s.Resume(); err != nil {
				t.Fatalf("returned callback retained resume gate: %v", err)
			}
		})
	}
}

// Stop cancels the service, closes admission and reports its own deadline
// without pretending an active recovery callback has drained.
func TestPausedInstanceRecoveryParticipatesInShutdownDrain(t *testing.T) {
	s := testService(t)
	recoverInstance := s.WithPausedInstance
	if err := s.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- recoverInstance(context.Background(), "a", func() error {
			close(entered)
			<-s.ctx.Done()
			close(cancelled)
			<-release
			return nil
		})
	}()
	awaitValue(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop reported drained active recovery: %v", err)
	}
	awaitValue(t, cancelled)
	if err := recoverInstance(context.Background(), "bb", func() error { t.Error("stopping service admitted recovery"); return nil }); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("stopping recovery admission: %v", err)
	}
	if err := s.Resume(); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("stopping service resumed: %v", err)
	}
	if _, err := s.Enqueue(context.Background(), Request{Kind: "test"}); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("stopping service admitted job: %v", err)
	}
	once.Do(func() { close(release) })
	if err := awaitValue(t, done); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("returned recovery did not drain: %v", err)
	}
}

func TestPausedInstanceRecoveryRejectsInvalidOrUnavailableAdmission(t *testing.T) {
	s := testService(t)
	recoverInstance := s.WithPausedInstance
	callback := func() error { t.Error("invalid or unavailable callback entered"); return nil }
	if err := recoverInstance(context.Background(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("unstarted recovery admission: %v", err)
	}
	if err := s.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "Uppercase", "has_underscore", "-leading"} {
		if err := recoverInstance(context.Background(), id, callback); domain.AsError(err).Code != domain.CodeValidationFailed {
			t.Fatalf("invalid recovery ID %q admitted: %v", id, err)
		}
	}
	if err := recoverInstance(context.Background(), "a", nil); domain.AsError(err).Code != domain.CodeValidationFailed {
		t.Fatalf("nil callback admitted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := recoverInstance(ctx, "a", callback); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller admitted: %v", err)
	}
	if err := s.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := recoverInstance(context.Background(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("resumed service admitted startup recovery: %v", err)
	}
	other := testService(t)
	recoverOther := other.WithPausedInstance
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	if err := other.StartPaused(parent); err != nil {
		t.Fatal(err)
	}
	cancelParent()
	if err := recoverOther(context.Background(), "a", callback); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("cancelled service admitted startup recovery: %v", err)
	}
}

// StartPaused normally fails interrupted running rows. If unexpected live
// running evidence appears during startup, recovery must not mutate over it.
func TestPausedInstanceRecoveryRejectsUnexpectedRunningWork(t *testing.T) {
	s := testService(t)
	recoverInstance := s.WithPausedInstance
	seedInstance(t, s, "a")
	if err := s.StartPaused(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at,payload) VALUES('unexpected','a','test','running','2026-10-16T00:00:00Z','{}')`); err != nil {
		t.Fatal(err)
	}
	if err := recoverInstance(context.Background(), "a", func() error { t.Error("recovery overlapped running job"); return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatalf("running work admitted recovery: %v", err)
	}
}
