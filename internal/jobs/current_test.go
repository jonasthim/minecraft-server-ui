package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestCheckCurrentUsesOriginalIdentityInsideMutation(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "alpha")
	result := make(chan error, 1)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		original := e.Job.ID
		e.Job.ID = "forged"
		e.Job.InstanceID = "foreign"
		e.Job.Kind = "foreign"
		e.Payload = []byte(`{"actor_id":99}`)
		copy := *e
		if _, err := copy.CheckCurrent(ctx); domain.AsError(err).Code != domain.CodeForbidden {
			return errors.New("copied execution admitted")
		}
		err := e.WithInstanceMutation(ctx, func(id string) error {
			snapshot, err := e.CheckCurrent(ctx)
			if err != nil {
				return err
			}
			if snapshot.JobID != original || snapshot.InstanceID != "alpha" || snapshot.Kind != "test" || snapshot.RequesterID != 1 {
				return errors.New("mutable metadata conferred authority")
			}
			return nil
		})
		result <- err
		return err
	})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	job := enqueue(t, s, "alpha")
	if err := awaitValue(t, result); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, job.ID, domain.JobSucceeded)
	if _, err := (&Execution{}).CheckCurrent(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("forged execution admitted", err)
	}
}
func TestCheckCurrentRejectsClosedAdmissionBeforeTerminalSQL(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "alpha")
	result := make(chan error, 1)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		if err := e.finishMutationAdmission(); err != nil {
			return err
		}
		var state string
		if err := s.db.QueryRowContext(ctx, "SELECT state FROM jobs WHERE id=?", e.Job.ID).Scan(&state); err != nil {
			return err
		}
		if state != "running" {
			return errors.New("test missed preterminal window")
		}
		_, err := e.CheckCurrent(ctx)
		result <- err
		return nil
	})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	job := enqueue(t, s, "alpha")
	if err := awaitValue(t, result); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("closed admission accepted", err)
	}
	waitState(t, s, job.ID, domain.JobSucceeded)
}
func TestCheckCurrentRejectsCancellationAndStopping(t *testing.T) {
	s := testService(t)
	seedInstance(t, s, "alpha")
	result := make(chan error, 1)
	mustRegister(t, s, "test", func(ctx context.Context, e *Execution) error {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := e.CheckCurrent(cancelled); !errors.Is(err, context.Canceled) {
			return errors.New("caller cancellation ignored")
		}
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
		_, err := e.CheckCurrent(ctx)
		s.mu.Lock()
		s.stopping = false
		s.mu.Unlock()
		result <- err
		return nil
	})
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	job := enqueue(t, s, "alpha")
	if err := awaitValue(t, result); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal("stopping service admitted", err)
	}
	waitState(t, s, job.ID, domain.JobSucceeded)
}
