package backup

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"time"
)

const MaxSaveLease = 5 * time.Minute
const OnlineSaveLease = MaxSaveLease - 2*time.Second

type leaseArmer interface {
	ArmSaveLease(context.Context, domain.ProcessIdentity, string, time.Time) error
}
type saveSession interface {
	Flush(context.Context) (domain.CommandResult, error)
	ReleaseSaving(context.Context, string) (domain.CommandResult, error)
}

// captureLease records the exact immutable deadline before any save-off attempt.
// The independent launcher watchdog remains responsible after manager death.
// A failed arm may have delivered save-off, so release is attempted on that path.
func captureLease(ctx context.Context, native leaseArmer, session saveSession, identity domain.ProcessIdentity, id string, duration time.Duration, journal func(context.Context, string, time.Time) error, copy func(context.Context) error) (err error) {
	if duration <= 0 || duration > MaxSaveLease {
		return domain.E(domain.CodeValidationFailed, "save lease must expire within five minutes")
	}
	expiry := time.Now().Add(duration)
	reserve := min(30*time.Second, duration/2)
	leaseCtx, cancel := context.WithDeadline(ctx, expiry.Add(-reserve))
	defer cancel()
	if err = journal(leaseCtx, "armed", expiry); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, releaseErr := session.ReleaseSaving(cleanup, id)
		if releaseErr == nil && !result.Verified {
			releaseErr = domain.E(domain.CodeUnavailable, "saving was not freshly acknowledged as enabled")
		}
		state := "released"
		if releaseErr != nil {
			state = "unresolved"
		}
		journalErr := journal(cleanup, state, expiry)
		err = errors.Join(err, releaseErr, journalErr)
	}()
	if err = native.ArmSaveLease(leaseCtx, identity, id, expiry); err != nil {
		return err
	}
	result, err := session.Flush(leaseCtx)
	if err != nil {
		return err
	}
	if !result.Verified {
		return domain.E(domain.CodeUnavailable, "save-all flush completion was not acknowledged")
	}
	if err = leaseCtx.Err(); err != nil {
		return err
	}
	if err = copy(leaseCtx); err != nil {
		return err
	}
	return leaseCtx.Err()
}
