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

	"modernc.org/sqlite"
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
		_, _ = fmt.Fprintln(os.Stderr, "native-update-schema-diagnostic:child:entered")
		cfg, err := config.FromEnvironment()
		if err != nil {
			return true, schemaProbeFailure("config", err)
		}
		schema, err := selfupdate.ReadOnlySchema(ctx, filepath.Join(cfg.DataDir, "manager.db"))
		if err != nil {
			return true, schemaProbeFailure("schema_read", err)
		}
		capability.Schema = schema
		challenge, challengeErr := selfupdate.ReadChallenge(ctx, cfg.DataDir)
		if challengeErr == nil {
			if challenge.FromVersion != version {
				return true, schemaProbeFailure("challenge_version", fmt.Errorf("schema probe challenge version mismatch"))
			}
			authorized, err := selfupdate.ReadOnlyActorAuthorized(ctx, filepath.Join(cfg.DataDir, "manager.db"), challenge.ActorID)
			if err != nil {
				return true, schemaProbeFailure("actor_read", err)
			}
			capability.ActorAuthorized = &authorized
		} else if !errors.Is(challengeErr, os.ErrNotExist) {
			return true, schemaProbeFailure("challenge_read", challengeErr)
		}
	}
	err := json.NewEncoder(out).Encode(capability)
	if args[0] == "update-schema" && err != nil {
		return true, schemaProbeFailure("encode", err)
	}
	return true, err
}

func schemaProbeFailure(stage string, err error) error {
	writeSchemaProbeFailure(os.Stderr, stage, err)
	return err
}

// The parent accepts only this fixed grammar from bounded stderr. Never pass
// error text, paths or challenge/request values into the diagnostic stream.
func writeSchemaProbeFailure(out io.Writer, stage string, err error) {
	if err == nil {
		return
	}
	switch stage {
	case "config", "schema_read", "challenge_read", "challenge_version", "actor_read", "encode":
	default:
		return
	}
	reason := "other"
	var sqliteErr *sqlite.Error
	switch {
	case errors.Is(err, os.ErrPermission):
		reason = "permission"
	case errors.Is(err, os.ErrNotExist):
		reason = "not_exist"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline"
	case errors.As(err, &sqliteErr) && sqliteErr.Code() > 0 && sqliteErr.Code() <= 65535:
		reason = fmt.Sprintf("sqlite:%d", sqliteErr.Code())
	}
	_, _ = fmt.Fprintln(out, "native-update-schema-diagnostic:"+stage+":"+reason)
}
