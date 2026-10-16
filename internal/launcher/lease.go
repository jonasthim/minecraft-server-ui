package launcher

import (
	"fmt"
	"time"
)

const MaxSaveLease = 5 * time.Minute

type leaseGuard struct {
	id       string
	deadline time.Time
	write    func(string) error
	failures int
}

func newLeaseGuard(write func(string) error) *leaseGuard { return &leaseGuard{write: write} }

// Caller holds the command serialization lock for every method. Installing the
// watchdog and save-off delivery are one operation; lease extension is forbidden.
func (l *leaseGuard) arm(id string, expires, now time.Time) error {
	if id == "" || len(id) > 128 || !expires.After(now) || expires.Sub(now) > MaxSaveLease {
		return fmt.Errorf("lease must expire within five minutes")
	}
	if l.id != "" {
		return fmt.Errorf("save lease already active")
	}
	l.id = id
	l.deadline = now.Add(expires.Sub(now))
	l.failures = 0
	if err := l.write("save-off"); err != nil {
		_ = l.write("save-on")
		return fmt.Errorf("save-off delivery failed; watchdog remains armed: %w", err)
	}
	return nil
}
func (l *leaseGuard) expire(now time.Time) error {
	if l.id == "" || now.Before(l.deadline) {
		return nil
	}
	return l.restore()
}
func (l *leaseGuard) release(id string, now time.Time) error {
	if l.id == "" || l.id != id {
		return fmt.Errorf("save lease mismatch")
	}
	expired := !now.Before(l.deadline)
	if err := l.restore(); err != nil {
		return err
	}
	if expired {
		return fmt.Errorf("save lease expired; save-on attempted")
	}
	return nil
}
func (l *leaseGuard) restore() error {
	if l.failures >= 30 {
		return fmt.Errorf("save-on retry limit reached")
	}
	if err := l.write("save-on"); err != nil {
		l.failures++
		return fmt.Errorf("save-on delivery failed: %w", err)
	}
	l.id = ""
	l.deadline = time.Time{}
	l.failures = 0
	return nil
}
