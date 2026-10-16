package backup

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
	"time"
)

type leaseFixture struct {
	steps       []string
	fail        string
	deadline    time.Time
	cleanupLive bool
}

func (f *leaseFixture) ArmSaveLease(ctx context.Context, identity domain.ProcessIdentity, id string, expiry time.Time) error {
	f.steps = append(f.steps, "arm")
	f.deadline = expiry
	if f.fail == "arm" {
		return errors.New("arm failure")
	}
	return nil
}
func (f *leaseFixture) Flush(ctx context.Context) (domain.CommandResult, error) {
	f.steps = append(f.steps, "flush")
	if f.fail == "flush" {
		return domain.CommandResult{}, errors.New("flush failure")
	}
	return domain.CommandResult{Received: true, Verified: true}, nil
}
func (f *leaseFixture) ReleaseSaving(ctx context.Context, id string) (domain.CommandResult, error) {
	f.steps = append(f.steps, "save-on")
	f.cleanupLive = ctx.Err() == nil
	if f.fail == "save-on" {
		return domain.CommandResult{}, errors.New("release failure")
	}
	return domain.CommandResult{Received: true, Verified: true}, nil
}
func TestLeaseAlwaysReleasesBeforeReturning(t *testing.T) {
	for _, fail := range []string{"", "arm", "flush", "copy", "save-on", "cancel"} {
		t.Run(fail, func(t *testing.T) {
			f := &leaseFixture{fail: fail}
			journal := func(ctx context.Context, state string, expiry time.Time) error {
				f.steps = append(f.steps, state)
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			start := time.Now()
			err := captureLease(ctx, f, f, domain.ProcessIdentity{InstanceID: "alpha"}, "lease", time.Minute, journal, func(ctx context.Context) error {
				f.steps = append(f.steps, "copy")
				if fail == "cancel" {
					cancel()
					return ctx.Err()
				}
				if fail == "copy" {
					return errors.New("copy failure")
				}
				return nil
			})
			if fail == "" && err != nil {
				t.Fatal(err)
			}
			if fail != "" && err == nil {
				t.Fatal("failure hidden")
			}
			if !f.cleanupLive {
				t.Fatalf("cleanup absent or canceled: %v", f.steps)
			}
			if f.deadline.Before(start) || f.deadline.Sub(start) > 5*time.Minute {
				t.Fatalf("unsafe deadline %s", f.deadline)
			}
			if f.steps[len(f.steps)-2] != "save-on" {
				t.Fatalf("release ordering %v", f.steps)
			}
		})
	}
}
func TestLeaseRejectsExtendedDeadline(t *testing.T) {
	f := &leaseFixture{}
	if err := captureLease(context.Background(), f, f, domain.ProcessIdentity{}, "lease", 6*time.Minute, func(context.Context, string, time.Time) error { return nil }, func(context.Context) error { return nil }); err == nil {
		t.Fatal("accepted extended lease")
	}
	if len(f.steps) != 0 {
		t.Fatal("armed rejected lease")
	}
}

func TestLeaseDoesNotArmWithoutDurableJournal(t *testing.T) {
	f := &leaseFixture{}
	err := captureLease(t.Context(), f, f, domain.ProcessIdentity{}, "lease", time.Minute, func(context.Context, string, time.Time) error { return errors.New("journal unavailable") }, func(context.Context) error { t.Fatal("copied before durable journal"); return nil })
	if err == nil || len(f.steps) != 0 {
		t.Fatalf("armed without journal: %v %v", f.steps, err)
	}
}
func TestLeasePanicStillAttemptsSaveOn(t *testing.T) {
	f := &leaseFixture{}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic hidden")
			}
		}()
		_ = captureLease(t.Context(), f, f, domain.ProcessIdentity{}, "lease", time.Minute, func(context.Context, string, time.Time) error { return nil }, func(context.Context) error { panic("interrupted") })
	}()
	if !f.cleanupLive || f.steps[len(f.steps)-1] != "save-on" {
		t.Fatalf("panic skipped saving recovery: %v", f.steps)
	}
}
