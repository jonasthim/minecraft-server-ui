package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

var version = "0.0.0-dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		if _, err := fmt.Fprintf(os.Stdout, "minecraft-ui-update %s\n", version); err != nil {
			os.Exit(1)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	if len(os.Args) < 2 || os.Args[1] != "service" {
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), 9*time.Minute)
	}
	defer cancel()
	if err := selfupdate.RunNativeUpdater(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "native manager update refused:", err)
		os.Exit(1)
	}
}
