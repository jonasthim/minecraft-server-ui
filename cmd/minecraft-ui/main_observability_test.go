package main

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestObservationFailureDrainsHTTPAndDoesNotExposePrivateError(t *testing.T) {
	for _, name := range []string{"metrics", "notifications"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := watchObservability(t.Context(), name, func(context.Context) error { return errors.New("private destination/host error") }, func(ctx context.Context) error {
				calls++
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
					t.Error("unbounded HTTP cleanup")
				}
				return nil
			}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if calls != 1 || domain.AsError(err).Code != domain.CodeUnavailable || domain.AsError(err).Message != name+" observation stopped unexpectedly" {
				t.Fatal(calls, err)
			}
		})
	}
}
func TestObservationShutdownWaitsForWorkerDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	shutdownCalls := 0
	go func() {
		done <- watchObservability(ctx, "metrics", func(ctx context.Context) error { close(started); <-ctx.Done(); <-release; return context.Canceled }, func(context.Context) error { shutdownCalls++; return nil }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatal("returned before worker drain", err)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil || shutdownCalls != 0 {
			t.Fatal(err, shutdownCalls)
		}
	case <-time.After(time.Second):
		t.Fatal("worker drain did not complete")
	}
}
