package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func TestUpdateStopGuardRejectsCopiedAndForgedExecutions(t *testing.T) {
	_, s, _, _, _ := setup(t)
	if _, _, err := s.BeginUpdateStop(t.Context(), &jobs.Execution{}, domain.User{ID: 1, Role: domain.RoleOperator}); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal("forged execution admitted", err)
	}
}
func TestUpdateStopGuardLaterStopAndConfigurationBlockNativeStart(t *testing.T) {
	for _, action := range []string{"stop", "configure", "once"} {
		t.Run(action, func(t *testing.T) {
			database, s, _, _, _ := setup(t)
			if _, err := database.ExecContext(t.Context(), "UPDATE users SET role='admin' WHERE id=1"); err != nil {
				t.Fatal(err)
			}
			actor := domain.User{ID: 1, Role: domain.RoleAdmin}
			queue := jobs.New(database, nil)
			s.config.Jobs = queue
			result := make(chan error, 1)
			var starts atomic.Int32
			if err := queue.Register("instance_update", func(ctx context.Context, e *jobs.Execution) error {
				copy := *e
				if _, _, err := s.BeginUpdateStop(ctx, &copy, actor); domain.AsError(err).Code != domain.CodeForbidden {
					return errors.New("copied update execution admitted")
				}
				epoch, guard, err := s.BeginUpdateStop(ctx, e, actor)
				if err != nil {
					return err
				}
				if epoch <= 0 {
					return errors.New("no exact committed epoch")
				}
				switch action {
				case "stop":
					err = s.CancelRecovery(ctx, "alpha", actor)
				case "configure":
					_, err = s.ConfigureRecovery(ctx, "alpha", true, actor)
				}
				if err != nil {
					return err
				}
				start := func() (domain.ProcessIdentity, error) {
					starts.Add(1)
					return domain.ProcessIdentity{InstanceID: "alpha"}, nil
				}
				err = e.WithInstanceMutation(ctx, func(id string) error {
					_, err := guard(ctx, id, start)
					if action == "once" {
						if err != nil {
							return err
						}
						if _, err = guard(ctx, id, start); domain.AsError(err).Code != domain.CodeForbidden {
							return errors.New("reused update guard admitted")
						}
						return nil
					}
					if domain.AsError(err).Code != domain.CodeConflict {
						return errors.New("later stop/configuration was ignored")
					}
					return nil
				})
				result <- err
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := queue.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer queue.Stop(context.Background())
			if _, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "instance_update", Payload: []byte(`{}`), RequestedBy: 1}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("update job did not finish")
			}
			expected := int32(0)
			if action == "once" {
				expected = 1
			}
			if starts.Load() != expected {
				t.Fatalf("native starts=%d expected=%d", starts.Load(), expected)
			}
		})
	}
}

func TestUpdateStartGuardSerializesNativeStartBeforeLaterStop(t *testing.T) {
	database, s, _, _, _ := setup(t)
	actor := domain.User{ID: 1, Role: domain.RoleOperator}
	queue := jobs.New(database, nil)
	s.config.Jobs = queue
	result := make(chan error, 1)
	stopResult := make(chan error, 1)
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	if err := queue.Register("instance_update", func(ctx context.Context, e *jobs.Execution) (out error) {
		defer func() { result <- out }()
		epoch, guard, err := s.BeginUpdateStop(ctx, e, actor)
		if err != nil {
			return err
		}
		return e.WithInstanceMutation(ctx, func(id string) error {
			_, err := guard(ctx, id, func() (domain.ProcessIdentity, error) {
				lock := s.recoveryLock(id)
				if lock.TryLock() {
					lock.Unlock()
					return domain.ProcessIdentity{}, errors.New("native start did not hold recovery intent mutex")
				}
				close(startEntered)
				<-releaseStart
				var actual int64
				if err := database.QueryRowContext(ctx, "SELECT epoch FROM crash_recovery WHERE instance_id=?", id).Scan(&actual); err != nil {
					return domain.ProcessIdentity{}, err
				}
				if actual != epoch {
					return domain.ProcessIdentity{}, errors.New("later stop passed guarded start")
				}
				return domain.ProcessIdentity{InstanceID: id}, nil
			})
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	if _, err := queue.Enqueue(t.Context(), jobs.Request{InstanceID: "alpha", Kind: "instance_update", Payload: []byte(`{}`), RequestedBy: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-startEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("start guard did not enter")
	}
	attempting := make(chan struct{})
	go func() { close(attempting); stopResult <- s.CancelRecovery(t.Context(), "alpha", actor) }()
	<-attempting
	close(releaseStart)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := <-stopResult; err != nil {
		t.Fatal(err)
	}
}
