package scheduler

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type nativeFake struct {
	id          domain.ProcessIdentity
	observation supervisor.Observation
	err         error
}

func (n *nativeFake) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	return n.id, n.err
}
func (n *nativeFake) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return n.observation, n.err
}
func TestRecoveryOnlyExactUnexpectedExitAndDurableStop(t *testing.T) {
	database, s, now, _, _ := setup(t)
	if _, e := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	native := &nativeFake{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "pid-generation-1", StartedAt: now.Add(-time.Hour)}}
	native.observation = supervisor.Observation{State: domain.StateRunning, ProcessRunning: true}
	s.config.Native = native
	user := domain.User{ID: 1, Role: domain.RoleAdmin}
	if _, e := s.ConfigureRecovery(t.Context(), "alpha", true, user); e != nil {
		t.Fatal(e)
	}
	if e := s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, e := s.Recovery(t.Context(), "alpha")
	if e != nil || len(status.Attempts) != 0 {
		t.Fatal(status, e)
	}
	native.observation = supervisor.Observation{State: domain.StateCrashed} // no final proof
	if e = s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, e = s.Recovery(t.Context(), "alpha")
	if e != nil || len(status.Attempts) != 0 {
		t.Fatal(status, e)
	}
	native.observation.Exit = &launcher.Exit{Identity: native.id, ExitedAt: *now, ExitCode: 1}
	if e = s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, e = s.Recovery(t.Context(), "alpha")
	if e != nil || len(status.Attempts) != 1 || !status.Attempts[0].DueAt.Equal(now.Add(10*time.Second)) {
		t.Fatal(status, e)
	}
	fresh := New(Config{DB: database, Now: func() time.Time { return *now }, HostTimezone: "UTC", Native: native})
	if e = fresh.CancelRecovery(t.Context(), "alpha", user); e != nil {
		t.Fatal(e)
	}
	if e = fresh.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, e = fresh.Recovery(t.Context(), "alpha")
	if e != nil || len(status.Attempts) != 1 || status.Attempts[0].State != "cancelled" {
		t.Fatal(status, e)
	}
}
func TestCrashAdmissionRollingWindowBoundary(t *testing.T) {
	database, s, now, _, _ := setup(t)
	if _, e := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ConfigureRecovery(t.Context(), "alpha", true, domain.User{ID: 1, Role: domain.RoleAdmin}); e != nil {
		t.Fatal(e)
	}
	for i, ts := range []string{"2026-10-16T11:50:00Z", "2026-10-16T11:55:00Z", "2026-10-16T11:59:00Z"} {
		_, e := database.Exec(`INSERT INTO crash_attempts(id,instance_id,epoch,actor_id,generation,identity_json,due_at,admitted_at,state) VALUES(?,'alpha',1,1,?,'{}',?,?,'admitted')`, string(rune('a'+i)), string(rune('a'+i)), stamp(at(ts)), stamp(at(ts)))
		if e != nil {
			t.Fatal(e)
		}
	}
	delay, allowed, e := s.retryDelay(t.Context(), "alpha", *now)
	if e != nil || allowed {
		t.Fatal(delay, allowed, e)
	}
	*now = now.Add(time.Nanosecond)
	delay, allowed, e = s.retryDelay(t.Context(), "alpha", *now)
	if e != nil || !allowed || delay != 60*time.Second {
		t.Fatal(delay, allowed, e)
	}
}
func TestRecoveryGuardRejectsStopAndChangedNativeGeneration(t *testing.T) {
	database, s, now, _, _ := setup(t)
	if _, e := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	native := &nativeFake{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "one", StartedAt: now.Add(-time.Hour)}}
	native.observation = supervisor.Observation{ProcessRunning: true}
	s.config.Native = native
	user := domain.User{ID: 1, Role: domain.RoleAdmin}
	if _, e := s.ConfigureRecovery(t.Context(), "alpha", true, user); e != nil {
		t.Fatal(e)
	}
	native.observation = supervisor.Observation{State: domain.StateCrashed, Exit: &launcher.Exit{Identity: native.id, ExitedAt: *now, ExitCode: 1}}
	if e := s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, _ := s.Recovery(t.Context(), "alpha")
	attempt := status.Attempts[0].ID
	*now = status.Attempts[0].DueAt
	starts := 0
	start := func() (domain.ProcessIdentity, error) { starts++; return native.id, nil }
	original := native.id
	native.id.StartIdentity = "manual-new-generation"
	if _, e := s.admitRecoveryStart(t.Context(), "alpha", attempt, start); e == nil || starts != 0 {
		t.Fatal(starts, e)
	}
	native.id = original
	if e := s.CancelRecovery(t.Context(), "alpha", user); e != nil {
		t.Fatal(e)
	}
	if _, e := s.admitRecoveryStart(t.Context(), "alpha", attempt, start); e == nil || starts != 0 {
		t.Fatal(starts, e)
	}
}
func TestRecoveryGuardPersistsAdmissionBeforeNativeIO(t *testing.T) {
	database, s, now, _, _ := setup(t)
	if _, e := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	native := &nativeFake{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "one", StartedAt: now.Add(-time.Hour)}}
	s.config.Native = native
	if _, e := s.ConfigureRecovery(t.Context(), "alpha", true, domain.User{ID: 1, Role: domain.RoleAdmin}); e != nil {
		t.Fatal(e)
	}
	native.observation = supervisor.Observation{State: domain.StateCrashed, Exit: &launcher.Exit{Identity: native.id, ExitedAt: *now, ExitCode: 1}}
	if e := s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, _ := s.Recovery(t.Context(), "alpha")
	attempt := status.Attempts[0].ID
	*now = status.Attempts[0].DueAt
	_, e := s.admitRecoveryStart(t.Context(), "alpha", attempt, func() (domain.ProcessIdentity, error) {
		var state, admitted string
		err := database.QueryRow(`SELECT state,admitted_at FROM crash_attempts WHERE id=?`, attempt).Scan(&state, &admitted)
		if err != nil || state != "admitted" || admitted == "" {
			t.Fatal(state, admitted, err)
		}
		return native.id, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.admitRecoveryStart(t.Context(), "alpha", attempt, func() (domain.ProcessIdentity, error) {
		t.Fatal("repeated consumed native start")
		return native.id, nil
	}); e == nil {
		t.Fatal("missing consumed-attempt error")
	}
}

type recoveryStartFake struct{ starts atomic.Int32 }

func (f *recoveryStartFake) StartForRecovery(ctx context.Context, e *jobs.Execution, actor domain.User, guard lifecycle.RecoveryStartGuard) error {
	return e.WithInstanceMutation(ctx, func(id string) error {
		_, err := guard(ctx, id, func() (domain.ProcessIdentity, error) {
			f.starts.Add(1)
			return domain.ProcessIdentity{}, errors.New("native launch failed")
		})
		return err
	})
}
func TestCrashRecoveryQueueAdmitsAtMostThreeWithIncreasingDelays(t *testing.T) {
	database, s, now, _, _ := setup(t)
	if _, e := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); e != nil {
		t.Fatal(e)
	}
	native := &nativeFake{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "one", StartedAt: now.Add(-time.Hour)}}
	native.observation = supervisor.Observation{State: domain.StateCrashed, Exit: &launcher.Exit{Identity: native.id, ExitedAt: *now, ExitCode: 1}}
	q := jobs.New(database, nil)
	defer q.Stop(context.Background())
	feature := &recoveryStartFake{}
	s.config.Native = native
	s.config.Jobs = q
	s.config.Lifecycle = feature
	if e := s.RegisterJobs(); e != nil {
		t.Fatal(e)
	}
	if e := q.Start(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ConfigureRecovery(t.Context(), "alpha", true, domain.User{ID: 1, Role: domain.RoleAdmin}); e != nil {
		t.Fatal(e)
	}
	for _, delay := range []time.Duration{10 * time.Second, 30 * time.Second, 60 * time.Second} {
		before := *now
		if e := s.ObserveRecovery(t.Context()); e != nil {
			t.Fatal(e)
		}
		status, e := s.Recovery(t.Context(), "alpha")
		if e != nil || !status.Attempts[0].DueAt.Equal(before.Add(delay)) {
			t.Fatal(status, e, delay)
		}
		*now = status.Attempts[0].DueAt
		if e = s.dispatchRecovery(t.Context()); e != nil {
			t.Fatal(e)
		}
		var job string
		if e = database.QueryRow(`SELECT job_id FROM crash_attempts WHERE id=?`, status.Attempts[0].ID).Scan(&job); e != nil {
			t.Fatal(e)
		}
		deadline := time.Now().Add(time.Second)
		for {
			j, e := q.Get(t.Context(), job)
			if e != nil {
				t.Fatal(e)
			}
			if j.State == domain.JobFailed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("retry did not finish", j)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if e := s.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := s.dispatchRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	status, e := s.Recovery(t.Context(), "alpha")
	if e != nil || len(status.Attempts) != 3 || feature.starts.Load() != 3 {
		t.Fatal(status, e, feature.starts.Load())
	}
}
func TestCrashPublisherRecoversLostAcknowledgementByExactGeneration(t *testing.T) {
	database, s, now, _, _ := setup(t)
	native := &nativeFake{id: domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "one", StartedAt: now.Add(-time.Hour)}}
	native.observation = supervisor.Observation{State: domain.StateCrashed, Exit: &launcher.Exit{Identity: native.id, ExitedAt: *now, ExitCode: 1}}
	seen := map[string]int{}
	publish := func(_ context.Context, event CrashEvent) error {
		seen[event.ID]++
		if seen[event.ID] == 1 {
			return errors.New("publisher ack lost")
		}
		return nil
	}
	s.config.Native = native
	s.config.OnCrash = publish
	if e := s.ObserveRecovery(t.Context()); e == nil {
		t.Fatal("lost publisher ack not reported")
	}
	fresh := New(Config{DB: database, Native: native, Now: func() time.Time { return *now }, OnCrash: publish})
	if e := fresh.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e := fresh.ObserveRecovery(t.Context()); e != nil {
		t.Fatal(e)
	}
	if len(seen) != 1 {
		t.Fatal(seen)
	}
	for _, n := range seen {
		if n != 2 {
			t.Fatal(seen)
		}
	}
}

func TestDeliberateStopWithUncertainIdentitySuspendsRecovery(t *testing.T) {
	for _, uncertainty := range []string{"lookup_failure", "missing_native", "invalid_identity"} {
		t.Run(uncertainty, func(t *testing.T) {
			database, s, now, _, _ := setup(t)
			if _, err := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			original := domain.ProcessIdentity{InstanceID: "alpha", PID: 123, StartIdentity: "stopped-generation", StartedAt: now.Add(-time.Hour)}
			native := &nativeFake{id: original, observation: supervisor.Observation{State: domain.StateCrashed, Exit: &launcher.Exit{Identity: original, ExitedAt: *now, ExitCode: 1}}}
			s.config.Native = native
			admin := domain.User{ID: 1, Role: domain.RoleAdmin}
			if _, err := s.ConfigureRecovery(t.Context(), "alpha", true, admin); err != nil {
				t.Fatal(err)
			}
			if err := s.ObserveRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			switch uncertainty {
			case "lookup_failure":
				native.err = errors.New("temporary identity failure")
			case "missing_native":
				s.config.Native = nil
			case "invalid_identity":
				native.id = domain.ProcessIdentity{InstanceID: "alpha"}
			}
			if err := s.CancelRecovery(t.Context(), "alpha", admin); err != nil {
				t.Fatal(err)
			}
			native.err = nil
			native.id = original
			fresh := New(Config{DB: database, Now: func() time.Time { return *now }, Native: native, HostTimezone: "UTC"})
			if err := fresh.ObserveRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			status, err := fresh.Recovery(t.Context(), "alpha")
			if err != nil || status.Enabled || !strings.Contains(status.Reason, "re-enable") || len(status.Attempts) != 1 || status.Attempts[0].State != "cancelled" {
				t.Fatal("uncertain stop permitted old-generation recovery", status, err)
			}
			// Restoring observations, even for a new generation, does not silently lift
			// the persisted suspension. Re-enablement requires an administrator action.
			native.id.StartIdentity = "later-generation"
			native.observation.Exit.Identity = native.id
			if err = fresh.ObserveRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			status, err = fresh.Recovery(t.Context(), "alpha")
			if err != nil || status.Enabled || len(status.Attempts) != 1 {
				t.Fatal(status, err)
			}
			if _, err = fresh.ConfigureRecovery(t.Context(), "alpha", true, domain.User{ID: 1, Role: domain.RoleOperator}); domain.AsError(err).Code != domain.CodeForbidden {
				t.Fatal(err)
			}
			if _, err = fresh.ConfigureRecovery(t.Context(), "alpha", true, admin); err != nil {
				t.Fatal(err)
			}
			if err = fresh.ObserveRecovery(t.Context()); err != nil {
				t.Fatal(err)
			}
			status, err = fresh.Recovery(t.Context(), "alpha")
			if err != nil || !status.Enabled || len(status.Attempts) != 2 {
				t.Fatal(status, err)
			}
		})
	}
}
