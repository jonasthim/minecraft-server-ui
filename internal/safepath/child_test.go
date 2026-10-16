package safepath

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestClosedParentPreservesHostCause(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	child, err := openChild(t.Context(), root, "child")
	if child != nil {
		child.Close()
		t.Fatal("closed parent opened child")
	}
	var pe *os.PathError
	if !errors.Is(err, os.ErrClosed) || !errors.As(err, &pe) {
		t.Fatal("closed host cause lost", err)
	}
}

type stepContext struct {
	context.Context
	calls    int
	cancelAt int
}

func (s *stepContext) Err() error {
	s.calls++
	if s.calls >= s.cancelAt {
		return context.Canceled
	}
	return nil
}
func TestCancellationDuringDescent(t *testing.T) {
	ctx := &stepContext{Context: t.Context(), cancelAt: 3}
	root, err := OpenRoot(ctx, t.TempDir())
	if root != nil {
		root.Close()
		t.Fatal("canceled descent returned root")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatal("descent cancellation lost", err)
	}
}
