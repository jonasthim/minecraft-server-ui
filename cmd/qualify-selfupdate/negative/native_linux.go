//go:build linux

package main

import (
	"context"
	"errors"
)

func probeService(context.Context) error         { return errors.New("Windows only") }
func unhealthyService(ctx context.Context) error { <-ctx.Done(); return nil }
