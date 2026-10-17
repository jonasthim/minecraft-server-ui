// This signed negative fixture reports its real embedded schema/capability and
// never opens an HTTP listener. It is not a manager or shipping release member.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
)

const version = "v0.0.3"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "update-capability":
		if json.NewEncoder(os.Stdout).Encode(selfupdate.Capability{Format: 1, Version: version, Schema: db.LatestVersion()}) != nil {
			os.Exit(1)
		}
	case "update-schema":
		schema, err := selfupdate.ReadOnlySchema(ctx, filepath.Join(os.Getenv("MINECRAFT_UI_DATA_DIR"), "manager.db"))
		if err != nil {
			os.Exit(1)
		}
		if json.NewEncoder(os.Stdout).Encode(selfupdate.Capability{Format: 1, Version: version, Schema: schema}) != nil {
			os.Exit(1)
		}
	case "update-schema-service":
		if probeService(ctx) != nil {
			os.Exit(1)
		}
	case "serve":
		if unhealthyService(ctx) != nil {
			os.Exit(1)
		}
	default:
		os.Exit(2)
	}
}
