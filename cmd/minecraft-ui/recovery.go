package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/config"
	"github.com/jonasthim/minecraft-server-ui/internal/recovery"
)

// runAdminReset is dispatched only by the local native CLI. Argument errors
// deliberately never quote supplied values: an accidental password argument
// must not be copied into logs or terminal output.
func runAdminReset(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	var username string
	var seenUsername, passwordStdin bool
	invalid := func() error {
		return errors.New("usage: admin-reset --username NAME --password-stdin (password only through private stdin)")
	}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--username":
			if seenUsername || i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return invalid()
			}
			i++
			username = args[i]
			seenUsername = true
		case strings.HasPrefix(args[i], "--username="):
			if seenUsername {
				return invalid()
			}
			username = strings.TrimPrefix(args[i], "--username=")
			seenUsername = true
		case args[i] == "--password-stdin":
			if passwordStdin {
				return invalid()
			}
			passwordStdin = true
		default:
			return invalid()
		}
	}
	if !seenUsername || username == "" || !passwordStdin {
		return invalid()
	}
	cfg, err := config.FromEnvironment()
	if err != nil {
		return errors.New("invalid process configuration for offline recovery")
	}
	if err = recovery.Reset(ctx, cfg.DataDir, username, input); err != nil {
		return err
	}
	if output != nil {
		if _, err = fmt.Fprintln(output, "Administrator password reset; all account sessions and personal access tokens revoked. Start the manager and sign in with the new password."); err != nil {
			return errors.New("password reset committed; unable to write confirmation")
		}
	}
	return nil
}
