// Package server dispatches offline verification for published server layouts.
package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/server/launchargs"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

// VerifyLaunch verifies provider artifacts and exact argv. The launcher must
// independently rehash the managed runtime before invoking this dispatcher.
func VerifyLaunch(ctx context.Context, dataDir, id string, spec domain.LaunchSpec) error {
	if err := domain.ValidateInstanceID(id); err != nil {
		return err
	}
	if len(spec.Env) != 0 {
		return fmt.Errorf("server environment overrides require an approved typed policy")
	}
	args, err := launchargs.StripMemory(spec.Args)
	if err != nil {
		return err
	}
	root := filepath.Join(dataDir, "instances", id, "server", ".manager", "artifacts") + string(filepath.Separator)
	if len(args) >= 2 && args[0] == "-jar" && strings.HasPrefix(args[1], root) {
		return providerutil.VerifyLaunch(ctx, dataDir, id, spec)
	}
	return loaderutil.VerifyLaunch(ctx, dataDir, id, spec)
}
