package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"io"
	"os"
	"path/filepath"
)

// runSelfUpdateProbe handles fixed, side-effect-free native installer/updater
// probes. The updater invokes these as the manager account, never as root.
func runSelfUpdateProbe(ctx context.Context, args []string, out io.Writer) (bool, error) {
	if len(args) == 0 || (args[0] != "update-capability" && args[0] != "update-schema" && args[0] != "update-schema-service") {
		return false, nil
	}
	if len(args) != 1 {
		return true, fmt.Errorf("update probes accept no arguments")
	}
	if args[0] == "update-schema-service" {
		return true, runSelfUpdateProbeService(ctx)
	}
	capability := selfupdate.Capability{Format: 1, Version: version, Schema: db.LatestVersion()}
	if args[0] == "update-schema" {
		cfg, err := config.FromEnvironment()
		if err != nil {
			return true, err
		}
		schema, err := selfupdate.ReadOnlySchema(ctx, filepath.Join(cfg.DataDir, "manager.db"))
		if err != nil {
			return true, err
		}
		capability.Schema = schema
		challenge, challengeErr := selfupdate.ReadChallenge(ctx, cfg.DataDir)
		if challengeErr == nil {
			if challenge.FromVersion != version {
				return true, fmt.Errorf("schema probe challenge version mismatch")
			}
			authorized, err := selfupdate.ReadOnlyActorAuthorized(ctx, filepath.Join(cfg.DataDir, "manager.db"), challenge.ActorID)
			if err != nil {
				return true, err
			}
			capability.ActorAuthorized = &authorized
		} else if !errors.Is(challengeErr, os.ErrNotExist) {
			return true, challengeErr
		}
	}
	return true, json.NewEncoder(out).Encode(capability)
}
