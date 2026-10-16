package main

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func TestPlayersFailureShutsDownHTTPAndReturnsSafeError(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	shutdown := 0
	err := watchPlayers(t.Context(), func(context.Context) error { return errors.New("private database path") }, func(ctx context.Context) error {
		shutdown++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded shutdown")
		}
		return nil
	}, log)
	if shutdown != 1 || domain.AsError(err).Code != domain.CodeUnavailable || strings.Contains(err.Error(), "private database") {
		t.Fatalf("shutdown=%d error=%v", shutdown, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = watchPlayers(ctx, func(ctx context.Context) error { return ctx.Err() }, func(context.Context) error { shutdown++; return nil }, log)
	if err != nil || shutdown != 1 {
		t.Fatalf("normal cancellation %v shutdown=%d", err, shutdown)
	}
}
