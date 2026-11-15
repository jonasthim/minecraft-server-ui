//go:build linux

package install

import (
	"context"
	"errors"
	"testing"
)

// /proc/locks is a seq_file read in several syscalls; concurrent lock churn can
// skip or repeat entries between reads. An exclusive flock has one holder, so
// a torn read can hide the holder but never invent a different one.
func TestLaunchLockOwnerRetriesTornProcLocksReads(t *testing.T) {
	const device = "00:2a:1234"
	holder := []byte("1: FLOCK  ADVISORY  WRITE 4242 " + device + " 0 EOF\n")
	missing := []byte("1: FLOCK  ADVISORY  WRITE 99 00:2a:9 0 EOF\n")
	duplicate := append(append([]byte{}, holder...), holder...)
	other := []byte("1: FLOCK  ADVISORY  WRITE 7 " + device + " 0 EOF\n")
	sequence := func(bodies ...[]byte) (launchLinuxConfig, *int) {
		calls := 0
		return launchLinuxConfig{readProc: func(name string, _ int64) ([]byte, error) {
			if name != "locks" {
				return nil, errors.New("unexpected proc read")
			}
			body := bodies[min(calls, len(bodies)-1)]
			calls++
			return body, nil
		}}, &calls
	}
	config, calls := sequence(missing, duplicate, holder)
	if err := readLaunchLinuxLockOwner(context.Background(), config, device, 4242); err != nil || *calls != 3 {
		t.Fatalf("torn reads were not retried to a consistent holder: err=%v calls=%d", err, *calls)
	}
	config, _ = sequence(missing)
	if err := readLaunchLinuxLockOwner(context.Background(), config, device, 4242); err == nil {
		t.Fatal("persistently absent holder accepted")
	}
	config, calls = sequence(other, holder)
	if err := readLaunchLinuxLockOwner(context.Background(), config, device, 4242); err == nil || *calls != 1 {
		t.Fatalf("a different holder must fail immediately: err=%v calls=%d", err, *calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	config, _ = sequence(missing, holder)
	if err := readLaunchLinuxLockOwner(ctx, config, device, 4242); err == nil {
		t.Fatal("cancelled context retried")
	}
}
