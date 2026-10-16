package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestHTTPShutdownFailurePreventsCleanManagerExit(t *testing.T) {
	// ListenAndServe returns ErrServerClosed as soon as Shutdown closes listeners,
	// even when draining active requests subsequently times out.
	if err := httpServeResult(http.ErrServerClosed, context.DeadlineExceeded); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed request drain became a clean exit: %v", err)
	}
	listenErr := errors.New("listen failed")
	err := httpServeResult(listenErr, context.DeadlineExceeded)
	if !errors.Is(err, listenErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost serve or shutdown failure: %v", err)
	}
	if err := httpServeResult(http.ErrServerClosed, nil); err != nil {
		t.Fatalf("successful drain returned error: %v", err)
	}
}
