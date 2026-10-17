package players

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type delayedListNative struct {
	playerStoppedGuard
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (n *delayedListNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	n.once.Do(func() { close(n.entered); <-n.release })
	return domain.ProcessIdentity{InstanceID: "test", PID: 123, StartIdentity: "same"}, nil
}
func (n *delayedListNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{}, nil
}

// Saved-file fallback is observational too: preserve the snapshot's provenance,
// accept durable user work, and let that work execute after generation readback.
func TestSavedListObservationAcceptsQueuedWork(t *testing.T) {
	s := fixture(t)
	if _, err := s.db.ExecContext(t.Context(), `UPDATE instances SET created_at='2026-10-16T00:00:00Z',updated_at='2026-10-16T00:00:00Z' WHERE id='test'`); err != nil {
		t.Fatal(err)
	}
	s.instances = instance.New(s.db, s.paths, &playerStoppedGuard{})
	dirs, err := s.paths.For("test")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dirs.Server, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dirs.Server, "whitelist.json"), []byte(`[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Alex"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	native := &delayedListNative{entered: make(chan struct{}), release: make(chan struct{})}
	s.native = native
	var once sync.Once
	defer once.Do(func() { close(native.release) })
	q := jobs.New(s.db, nil)
	s.queue = q
	started := make(chan string, 1)
	if err = q.Register("test", func(_ context.Context, e *jobs.Execution) error { started <- e.Job.ID; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = q.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Release first on any assertion failure so queue shutdown can drain honestly.
	defer q.Stop(context.Background())
	defer once.Do(func() { close(native.release) })
	type result struct {
		list console.PlayerList
		err  error
	}
	done := make(chan result, 1)
	go func() { list, err := s.Lists(t.Context(), "test", "whitelist"); done <- result{list, err} }()
	select {
	case <-native.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("saved list did not enter native generation check")
	}
	job, err := q.Enqueue(t.Context(), jobs.Request{InstanceID: "test", Kind: "test"})
	if err != nil {
		t.Fatalf("saved read rejected user work: %v", err)
	}
	persisted, err := q.Get(t.Context(), job.ID)
	if err != nil || persisted.State != domain.JobQueued {
		t.Fatalf("job not durably deferred: %+v %v", persisted, err)
	}
	once.Do(func() { close(native.release) })
	select {
	case got := <-done:
		if got.err != nil || got.list.Source != "saved_native_json" || got.list.RuntimeVerified || len(got.list.Entries) != 1 || got.list.Entries[0].Player.Name != "Alex" {
			t.Fatalf("saved list changed: %+v %v", got.list, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("saved list did not finish")
	}
	select {
	case id := <-started:
		if id != job.ID {
			t.Fatal(id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued work did not run after saved read")
	}
}
