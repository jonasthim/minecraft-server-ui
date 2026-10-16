package instance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

type composedJobGuard struct {
	nativeCalls atomic.Int32
	native      MutationGuard
}

func (g *composedJobGuard) WithStoppedForJob(ctx context.Context, e *jobs.Execution, fn func(string) error) error {
	return e.WithInstanceMutation(ctx, func(id string) error {
		g.nativeCalls.Add(1)
		return g.native.WithStopped(ctx, id, func() error { return fn(id) })
	})
}

type queueIdleGuard struct {
	queue  *jobs.Service
	native MutationGuard
}

func (g queueIdleGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	return g.queue.WithIdle(ctx, id, func() error { return g.native.WithStopped(ctx, id, fn) })
}
func waitInstanceJob(t *testing.T, q *jobs.Service, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, e := q.Get(context.Background(), id)
		if e != nil {
			t.Fatal(e)
		}
		if j.State == domain.JobSucceeded || j.State == domain.JobFailed || j.State == domain.JobCancelled {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not finish")
}
func TestStoppedJobSessionUsesPrivateExecutionAndExpires(t *testing.T) {
	s, p, native := setup(t)
	ctx := context.Background()
	_, err := s.Create(ctx, admin, draft("alpha", 25565))
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.New(s.db, nil)
	guard := &composedJobGuard{native: native}
	s = New(s.db, p, queueIdleGuard{q, native}, WithJobMutationGuard(guard))
	sessions := make(chan *StoppedSession, 1)
	release := make(chan struct{})
	if err = q.Register("session", func(ctx context.Context, e *jobs.Execution) error {
		e.Job.InstanceID = "foreign"
		return s.WithStoppedJob(ctx, e, func(session *StoppedSession) error {
			d, err := session.Detail(ctx)
			if err != nil {
				return err
			}
			if d.Instance.ID != "alpha" {
				return errors.New("public execution redirected session")
			}
			paths, err := session.Paths(ctx)
			if err != nil {
				return err
			}
			if paths.Root != filepath.Join(p.dataDir, "instances", "alpha") {
				return errors.New("wrong paths")
			}
			copy := *session
			if err = copy.Materialize(ctx); domain.AsError(err).Code != domain.CodeForbidden {
				return errors.New("copied session accepted")
			}
			if err = session.Materialize(ctx); err != nil {
				return err
			}
			sessions <- session
			<-release
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err = q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	job, err := q.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: "session", RequestedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	var retained *StoppedSession
	select {
	case retained = <-sessions:
	case <-time.After(3 * time.Second):
		t.Fatal("no session")
	}
	other := make(chan error, 1)
	go func() { _, err := s.Create(ctx, admin, draft("bravo", 25566)); other <- err }()
	select {
	case err = <-other:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("provider callback held a global instance mutex")
	}
	if err = s.Materialize(ctx, "alpha"); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("ordinary idle guard bypassed current job", err)
	}
	close(release)
	waitInstanceJob(t, q, job.ID)
	if err = retained.Materialize(ctx); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("retained session used after callback", err)
	}
	if guard.nativeCalls.Load() != 1 {
		t.Fatal("nested native lock", guard.nativeCalls.Load())
	}
	if err = s.WithStoppedJob(ctx, &jobs.Execution{}, func(*StoppedSession) error { return nil }); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("forged execution accepted", err)
	}
	var zero StoppedSession
	if _, err = zero.Paths(ctx); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	dirs, _ := p.For("alpha")
	if _, err = os.Stat(filepath.Join(dirs.Server, "server.properties")); err != nil {
		t.Fatal(err)
	}
}
func TestStoppedJobSessionReleasesOnErrorPanicAndCancellation(t *testing.T) {
	for _, mode := range []string{"error", "panic", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, p, native := setup(t)
			ctx := context.Background()
			if _, err := s.Create(ctx, admin, draft("alpha", 25565)); err != nil {
				t.Fatal(err)
			}
			q := jobs.New(s.db, nil)
			guard := &composedJobGuard{native: native}
			s = New(s.db, p, queueIdleGuard{q, native}, WithJobMutationGuard(guard))
			var retained *StoppedSession
			if err := q.Register("session", func(ctx context.Context, e *jobs.Execution) error {
				return s.WithStoppedJob(ctx, e, func(session *StoppedSession) error {
					retained = session
					switch mode {
					case "panic":
						panic("test panic")
					case "cancel":
						cancelled, cancel := context.WithCancel(ctx)
						cancel()
						return session.Materialize(cancelled)
					default:
						return errors.New("failure")
					}
				})
			}); err != nil {
				t.Fatal(err)
			}
			if err := q.Start(ctx); err != nil {
				t.Fatal(err)
			}
			defer q.Stop(context.Background())
			j, err := q.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: "session", RequestedBy: 1})
			if err != nil {
				t.Fatal(err)
			}
			waitInstanceJob(t, q, j.ID)
			if retained == nil {
				t.Fatal("callback never ran")
			}
			if err = retained.Materialize(ctx); domain.AsError(err).Code != domain.CodeForbidden {
				t.Fatal(err)
			}
			if err = s.Materialize(ctx, "alpha"); err != nil {
				t.Fatal("idle admission not released", err)
			}
		})
	}
}

func TestStoppedSessionRejectsConcurrentUseAndDrainsBeforeUnlock(t *testing.T) {
	s, p, native := setup(t)
	ctx := context.Background()
	if _, err := s.Create(ctx, admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	q := jobs.New(s.db, nil)
	guard := &composedJobGuard{native: native}
	s = New(s.db, p, queueIdleGuard{q, native}, WithJobMutationGuard(guard))
	available := make(chan *StoppedSession, 1)
	leave := make(chan struct{})
	returned := make(chan struct{})
	if err := q.Register("session", func(ctx context.Context, e *jobs.Execution) error {
		defer close(returned)
		return s.WithStoppedJob(ctx, e, func(session *StoppedSession) error { available <- session; <-leave; return nil })
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	j, err := q.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: "session", RequestedBy: 1})
	if err != nil {
		t.Fatal(err)
	}
	var session *StoppedSession
	select {
	case session = <-available:
	case <-time.After(3 * time.Second):
		t.Fatal("session not admitted")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before := s.db.Stats().WaitCount
	methodDone := make(chan error, 1)
	go func() { _, err := session.Detail(ctx); methodDone <- err }()
	deadline := time.Now().Add(3 * time.Second)
	for s.db.Stats().WaitCount == before && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount == before {
		t.Fatal("detail did not enter DB wait")
	}
	if err = session.Materialize(ctx); domain.AsError(err).Code != domain.CodeInstanceBusy {
		t.Fatal("concurrent method accepted", err)
	}
	close(leave)
	select {
	case <-returned:
		t.Fatal("native boundary released before admitted method drained")
	default:
	}
	tx.Rollback()
	if err = <-methodDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("session failed to drain")
	}
	if _, err = session.Paths(ctx); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("drained session retained authority", err)
	}
	waitInstanceJob(t, q, j.ID)
}
