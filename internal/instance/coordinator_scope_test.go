package instance

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestCoordinatorScopeRejectsForgeryCopyExpiryAndNestedSession(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	var retained *TransactionScope
	check := func(err error) {
		t.Helper()
		if domain.AsError(err).Code != domain.CodeForbidden {
			t.Fatalf("invalid capability admitted: %v", err)
		}
	}
	check((&TransactionScope{}).WithUse(t.Context(), func(context.Context, ScopeSnapshot) error { return nil }))
	err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			retained = scope
			copy := *scope
			check(copy.WithUse(ctx, func(context.Context, ScopeSnapshot) error { return nil }))
			if _, err := session.Detail(ctx); domain.AsError(err).Code != domain.CodeInstanceBusy {
				return errors.New("parent session reentered")
			}
			return scope.WithUse(ctx, func(ctx context.Context, snapshot ScopeSnapshot) error {
				if snapshot.Detail.Instance.ID != "alpha" || snapshot.Paths.Server == "" {
					return errors.New("scope identity mismatch")
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	check(retained.WithUse(t.Context(), func(context.Context, ScopeSnapshot) error { return nil }))
}

func TestCoordinatorScopeDrainsDetachedUseBeforeReleasingStoppedSession(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- s.withStoppedSession(context.Background(), "alpha", func(session *StoppedSession) error {
			return session.WithCoordinatorScope(context.Background(), func(ctx context.Context, scope *TransactionScope) error {
				go func() {
					_ = scope.WithUse(ctx, func(context.Context, ScopeSnapshot) error {
						close(entered)
						<-release
						return errors.New("detached operation failed")
					})
				}()
				<-entered
				return nil
			})
		})
	}()
	<-entered
	select {
	case err := <-returned:
		t.Fatalf("returned before drain: %v", err)
	default:
	}
	close(release)
	if err := <-returned; err == nil {
		t.Fatal("detached failure lost")
	}
}

func TestCoordinatorCommitBindsOriginalDatabaseAndRejectsForeignTransaction(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	var retainedTx *sql.Tx
	err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			fake := new(sql.Tx)
			if err := scope.WithCommitUse(ctx, fake, func(context.Context, ScopeSnapshot) error { return nil }); domain.AsError(err).Code != domain.CodeForbidden {
				return errors.New("foreign tx admitted")
			}
			return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error {
				retainedTx = tx
				if err := scope.WithUse(ctx, func(context.Context, ScopeSnapshot) error { return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
					return errors.New("DB reentry phase admitted")
				}
				if err := scope.WithCommit(ctx, func(context.Context, *sql.Tx) error { return nil }); domain.AsError(err).Code != domain.CodeInstanceBusy {
					return errors.New("nested commit admitted")
				}
				return scope.WithCommitUse(ctx, tx, func(ctx context.Context, snapshot ScopeSnapshot) error {
					_, err := tx.ExecContext(ctx, "UPDATE instances SET name='coordinator' WHERE id=?", snapshot.Detail.Instance.ID)
					return err
				})
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.Name != "coordinator" {
		t.Fatalf("private DB not committed: %v %v", detail, err)
	}
	if _, err = retainedTx.ExecContext(t.Context(), "SELECT 1"); !errors.Is(err, sql.ErrTxDone) {
		t.Fatal("transaction escaped callback", err)
	}
}

func TestCoordinatorCommitDrainsDetachedParticipantBeforeCommit(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- s.withStoppedSession(context.Background(), "alpha", func(session *StoppedSession) error {
			return session.WithCoordinatorScope(context.Background(), func(ctx context.Context, scope *TransactionScope) error {
				return scope.WithCommit(ctx, func(ctx context.Context, tx *sql.Tx) error {
					go func() {
						_ = scope.WithCommitUse(ctx, tx, func(context.Context, ScopeSnapshot) error {
							close(entered)
							<-release
							_, err := tx.ExecContext(ctx, "UPDATE instances SET name='drained' WHERE id='alpha'")
							return err
						})
					}()
					<-entered
					return nil
				})
			})
		})
	}()
	<-entered
	select {
	case err := <-returned:
		t.Fatalf("commit did not drain: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	close(release)
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.Name != "drained" {
		t.Fatal("detached tx operation lost", err)
	}
}

func TestCoordinatorUseReceivesCancellationFromOriginalScope(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.withStoppedSession(parent, "alpha", func(session *StoppedSession) error {
		return session.WithCoordinatorScope(parent, func(ctx context.Context, scope *TransactionScope) error {
			return scope.WithUse(context.Background(), func(linked context.Context, _ ScopeSnapshot) error {
				cancel()
				select {
				case <-linked.Done():
					return nil
				case <-time.After(time.Second):
					return errors.New("scope cancellation not delivered")
				}
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorAllowsOnlyOneCommitBoundary(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	err := s.withStoppedSession(t.Context(), "alpha", func(session *StoppedSession) error {
		return session.WithCoordinatorScope(t.Context(), func(ctx context.Context, scope *TransactionScope) error {
			if err := scope.WithCommit(ctx, func(context.Context, *sql.Tx) error { return nil }); err != nil {
				return err
			}
			if err := scope.WithCommit(ctx, func(context.Context, *sql.Tx) error { return nil }); err == nil {
				return errors.New("second commit admitted")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestCoordinatorPropagatesDetachedCommitFailure(t *testing.T) {
	s, _, _ := setup(t)
	if _, err := s.Create(t.Context(), admin, draft("alpha", 25565)); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan error, 1)
	go func() {
		returned <- s.withStoppedSession(context.Background(), "alpha", func(session *StoppedSession) error {
			return session.WithCoordinatorScope(context.Background(), func(ctx context.Context, scope *TransactionScope) error {
				go func() {
					_ = scope.WithCommit(ctx, func(context.Context, *sql.Tx) error {
						close(entered)
						<-release
						return errors.New("detached commit failure")
					})
				}()
				<-entered
				return nil
			})
		})
	}()
	<-entered
	close(release)
	if err := <-returned; err == nil {
		t.Fatal("detached commit failure lost")
	}
}
