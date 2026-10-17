package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
)

// runManagerState implements the no-server manager-state command grammar.
func runManagerState(ctx context.Context, args []string, output io.Writer) error {
	var operation, id string
	switch {
	case len(args) == 1 && args[0] == "snapshot":
		operation = "snapshot"
	case len(args) == 3 && args[0] == "verify" && args[1] == "--snapshot" && validManagerStateID(args[2]):
		operation, id = "verify", args[2]
	default:
		return errors.New("usage: manager-state snapshot | manager-state verify --snapshot HEX_ID")
	}
	cfg, err := config.FromEnvironment()
	if err != nil {
		return errors.New("manager state configuration is invalid")
	}
	var summary managerstate.Summary
	if operation == "snapshot" {
		summary, err = managerstate.Snapshot(ctx, cfg.DataDir, version)
	} else {
		summary, err = managerstate.Verify(ctx, cfg.DataDir, id)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(summary); err != nil {
		return errors.New("manager state summary could not be written")
	}
	return nil
}

func validManagerStateID(id string) bool {
	if len(id) != 64 || strings.ToLower(id) != id {
		return false
	}
	for _, char := range id {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}
