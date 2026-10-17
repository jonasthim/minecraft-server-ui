package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/install"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

var version = "0.0.0-dev"

type commandBackend struct {
	installer func(context.Context, []string, io.Writer) (bool, error)
	startup   func(context.Context) (bool, error)
	updater   func(context.Context, []string, io.Writer) error
}

func run(ctx context.Context, args []string, out io.Writer, backend commandBackend) error {
	if len(args) == 1 && args[0] == "version" {
		_, err := fmt.Fprintf(out, "minecraft-ui-update %s\n", version)
		return err
	}
	if handled, err := backend.installer(ctx, args, out); handled || err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "recover" {
		// The root recovery unit invokes this before every manager start. A
		// live installer worker owns the updater lock while awaiting this unit;
		// only its verified launch permit may bypass ordinary update recovery.
		if allowed, err := backend.startup(ctx); allowed || err != nil {
			return err
		}
	}
	return backend.updater(ctx, args, out)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	budget := 9 * time.Minute
	if len(os.Args) > 1 && os.Args[1] == "install" {
		budget = 15 * time.Minute
	}
	cancel := func() {}
	if len(os.Args) < 2 || os.Args[1] != "service" {
		ctx, cancel = context.WithTimeout(ctx, budget)
	}
	defer cancel()
	backend := commandBackend{install.RunInstallerCommand, install.CheckInstallerStartup, selfupdate.RunNativeUpdater}
	if err := run(ctx, os.Args[1:], os.Stdout, backend); err != nil {
		fmt.Fprintln(os.Stderr, "native manager operation refused:", err)
		os.Exit(1)
	}
}
