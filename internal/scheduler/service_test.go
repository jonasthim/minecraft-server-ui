package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"sync"
	"testing"
	"time"
)

type admissionFake struct {
	mu        sync.Mutex
	seen      map[string]string
	calls     int
	failAfter bool
}

func (f *admissionFake) EnqueueCommandOnce(ctx context.Context, key, id, text string, u domain.User) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	job, ok := f.seen[key]
	if !ok {
		job = "job-" + key
		f.seen[key] = job
	}
	if f.failAfter {
		f.failAfter = false
		return jobs.Job{}, errors.New("ack lost")
	}
	return jobs.Job{Job: domain.Job{ID: job, InstanceID: id, State: domain.JobQueued}}, nil
}
func setup(t *testing.T) (*sql.DB, *Service, *time.Time, *admissionFake, domain.User) {
	t.Helper()
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	_, e = database.Exec(`INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port) VALUES('alpha','Alpha','vanilla','1.21.9',21,1024,25565)`)
	if e != nil {
		t.Fatal(e)
	}
	_, e = database.Exec(`INSERT INTO users(id,username,role,disabled,created_at,updated_at) VALUES(1,'operator','operator',0,'2026-10-16T00:00:00Z','2026-10-16T00:00:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	now := at("2026-10-16T12:00:00Z")
	producer := &admissionFake{seen: map[string]string{}}
	s := New(Config{DB: database, HostTimezone: "UTC", Now: func() time.Time { return now }, Commands: producer})
	return database, s, &now, producer, domain.User{ID: 1, Role: domain.RoleOperator}
}
func TestScheduleClaimRecoversLostAdmissionAcknowledgement(t *testing.T) {
	database, s, now, producer, user := setup(t)
	saved, e := s.Save(t.Context(), "alpha", "", Input{Name: "List", Enabled: true, Action: domain.ScheduleCommand, Command: "list", Expression: "* * * * *"}, user)
	if e != nil {
		t.Fatal(e)
	}
	*now = at("2026-10-16T12:01:00Z")
	producer.failAfter = true
	if e = s.Tick(t.Context()); e == nil {
		t.Fatal("lost ack not reported")
	}
	fresh := New(Config{DB: database, Now: func() time.Time { return *now }, HostTimezone: "UTC", Commands: producer})
	if e = fresh.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	history, e := fresh.History(t.Context(), "alpha", saved.ID)
	if e != nil || len(history) != 1 || history[0].State != "admitted" || len(producer.seen) != 1 {
		t.Fatal(history, e, producer.seen)
	}
	*now = at("2026-10-16T12:02:00Z")
	if e = fresh.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	history, e = fresh.History(t.Context(), "alpha", saved.ID)
	if e != nil || len(history) != 2 || history[0].State != "skipped_overlap" || producer.calls != 2 {
		t.Fatal(history, e, producer.calls)
	}
}
func TestScheduleReconcileSkipsDowntimeAndRechecksAuthority(t *testing.T) {
	database, s, now, producer, user := setup(t)
	saved, e := s.Save(t.Context(), "alpha", "", Input{Name: "List", Enabled: true, Action: domain.ScheduleCommand, Command: "list", Expression: "* * * * *"}, user)
	if e != nil {
		t.Fatal(e)
	}
	*now = at("2026-10-16T14:00:00Z")
	if e = s.Reconcile(t.Context()); e != nil {
		t.Fatal(e)
	}
	list, e := s.List(t.Context(), "alpha")
	if e != nil || len(list) != 1 || !list[0].NextRunAt.Equal(at("2026-10-16T14:01:00Z")) || producer.calls != 0 {
		t.Fatal(list, e)
	}
	history, e := s.History(t.Context(), "alpha", saved.ID)
	if e != nil || len(history) != 1 || history[0].State != "skipped_downtime" {
		t.Fatal(history, e)
	}
	if _, e = database.Exec(`UPDATE users SET disabled=1 WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	*now = at("2026-10-16T14:01:00Z")
	if e = s.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	list, e = s.List(t.Context(), "alpha")
	if e != nil || list[0].Enabled || list[0].DisabledReason == "" || producer.calls != 0 {
		t.Fatal(list, e)
	}
}
func TestScheduleAutomaticUpdateUnavailableAndBounds(t *testing.T) {
	_, s, _, _, user := setup(t)
	in := Input{Name: "Updates", Enabled: true, Action: domain.ScheduleUpdate, Expression: "@daily"}
	if _, e := s.Save(t.Context(), "alpha", "", in, user); domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatal(e)
	}
	in.Enabled = false
	if _, e := s.Save(t.Context(), "alpha", "", in, user); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Save(t.Context(), "../alpha", "", in, user); e == nil {
		t.Fatal("unsafe instance")
	}
}
func TestConcurrentSchedulersShareOneOccurrenceIdentity(t *testing.T) {
	database, s, now, producer, user := setup(t)
	saved, e := s.Save(t.Context(), "alpha", "", Input{Name: "List", Enabled: true, Action: domain.ScheduleCommand, Command: "list", Expression: "* * * * *"}, user)
	if e != nil {
		t.Fatal(e)
	}
	*now = at("2026-10-16T12:01:00Z")
	other := New(Config{DB: database, Now: func() time.Time { return *now }, HostTimezone: "UTC", Commands: producer})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, worker := range []*Service{s, other} {
		wg.Add(1)
		go func(worker *Service) { defer wg.Done(); errs <- worker.Tick(t.Context()) }(worker)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	history, e := s.History(t.Context(), "alpha", saved.ID)
	if e != nil || len(history) != 1 || history[0].State != "admitted" || len(producer.seen) != 1 {
		t.Fatal(history, e, producer.seen)
	}
}
