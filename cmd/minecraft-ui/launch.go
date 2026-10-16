package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// runLaunch is dispatched by the integrator before the normal serve parser.
func runLaunch(ctx context.Context, args []string, stdin io.Reader) error {
	flags := flag.NewFlagSet("launch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dataDir := flags.String("data-dir", "", "absolute manager data directory")
	id := flags.String("instance", "", "instance id")
	timeout := flags.Duration("stop-timeout", 90*time.Second, "graceful stop timeout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected launch arguments")
	}
	return launcher.Run(ctx, launcher.Config{DataDir: *dataDir, InstanceID: *id, StopTimeout: *timeout, StopOnStdinEOF: runtime.GOOS == "windows"}, stdin)
}
