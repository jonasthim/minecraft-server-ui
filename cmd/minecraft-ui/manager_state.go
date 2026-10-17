package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/managerstate"
)

// runManagerState implements the no-server manager-state command grammar.
func runManagerState(ctx context.Context, args []string, output io.Writer) error {
	var operation, id string
	var fromSchema, toSchema int64
	switch {
	case len(args) == 1 && args[0] == "snapshot":
		operation = "snapshot"
	case len(args) == 3 && args[0] == "verify" && args[1] == "--snapshot" && validManagerStateID(args[2]):
		operation, id = "verify", args[2]
	case len(args) == 7 && args[0] == "migrate" && args[1] == "--snapshot" && validManagerStateID(args[2]) &&
		args[3] == "--from-schema" && parseManagerSchema(args[4], &fromSchema) &&
		args[5] == "--to-schema" && parseManagerSchema(args[6], &toSchema):
		operation, id = "migrate", args[2]
	case len(args) == 4 && args[0] == "resume" && args[1] == "--snapshot" && validManagerStateID(args[2]) &&
		args[3] == "--confirm-manager-state-review":
		operation, id = "resume", args[2]
	default:
		return errors.New("usage: manager-state snapshot | verify --snapshot HEX_ID | migrate --snapshot HEX_ID --from-schema N --to-schema M | resume --snapshot HEX_ID --confirm-manager-state-review")
	}
	cfg, err := config.FromEnvironment()
	if err != nil {
		return errors.New("manager state configuration is invalid")
	}
	var summary managerstate.Summary
	if operation == "snapshot" {
		summary, err = managerstate.Snapshot(ctx, cfg.DataDir, version)
		if err != nil {
			return err
		}
		return writeManagerStateSummary(output, summary)
	}
	if operation == "verify" {
		summary, err = managerstate.Verify(ctx, cfg.DataDir, id)
		if err != nil {
			return err
		}
		return writeManagerStateSummary(output, summary)
	}
	var maintenance managerstate.MaintenanceSummary
	switch operation {
	case "migrate":
		maintenance, err = managerstate.Migrate(ctx, cfg.DataDir, version, managerstate.MigrationOptions{
			SnapshotID: id, FromSchema: fromSchema, ToSchema: toSchema,
		})
		if err != nil {
			return err
		}
	case "resume":
		maintenance, err = managerstate.Resume(ctx, cfg.DataDir, version, id, true)
		if err != nil {
			return err
		}
	default:
		return errors.New("manager state operation is invalid")
	}
	return writeManagerStateSummary(output, maintenance)
}

func writeManagerStateSummary(output io.Writer, summary any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(summary); err != nil {
		return errors.New("manager state summary could not be written")
	}
	return nil
}

func parseManagerSchema(input string, output *int64) bool {
	value, err := strconv.ParseInt(input, 10, 64)
	if err != nil || value <= 0 || strconv.FormatInt(value, 10) != input {
		return false
	}
	*output = value
	return true
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
