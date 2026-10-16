//go:build !windows

package main

import (
	"context"
	"fmt"
)

func runSelfUpdateProbeService(context.Context) error {
	return fmt.Errorf("the fixed schema probe service is Windows-only")
}
