package launcher

import (
	"context"
	"fmt"
	"runtime"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
	"github.com/jonasthim/minecraft-server-ui/internal/server"
)

// verifyManagedRuntime is deliberately network-free. Get rehashes the immutable
// runtime tree and validates persisted official-publisher provenance; a stored
// executable path or archive checksum alone is not sufficient.
func verifyManagedRuntime(ctx context.Context, cfg Config, spec domain.LaunchSpec) error {
	if spec.JavaRuntimeID == "" {
		return fmt.Errorf("managed runtime identity is required")
	}
	verified, err := managedruntime.New(cfg.DataDir, nil).Get(ctx, spec.JavaRuntimeID)
	if err != nil {
		return fmt.Errorf("verify managed runtime: %w", err)
	}
	if verified.OS != runtime.GOOS || verified.Architecture != "x64" || !equalExecutable(spec.Executable, verified.Executable) || spec.JavaChecksum != verified.Artifact.Checksum {
		return fmt.Errorf("launch specification differs from verified managed runtime")
	}
	return nil
}

func verifyManagedLaunch(ctx context.Context, cfg Config, spec domain.LaunchSpec) error {
	if err := verifyManagedRuntime(ctx, cfg, spec); err != nil {
		return err
	}
	if err := server.VerifyLaunch(ctx, cfg.DataDir, cfg.InstanceID, spec); err != nil {
		return fmt.Errorf("verify immutable server launch: %w", err)
	}
	return nil
}

// Seal verifies a complete production launch offline before replacing activation.
// The caller must already hold stopped mutation exclusion; Seal takes no lock.
func Seal(ctx context.Context, dataDir, id string, spec domain.LaunchSpec) error {
	cfg := Config{DataDir: dataDir, InstanceID: id}
	paths, err := Paths(dataDir, id)
	if err != nil {
		return err
	}
	if err = ValidateSpec(spec, paths); err != nil {
		return err
	}
	if err = verifyManagedLaunch(ctx, cfg, spec); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return WriteSpec(dataDir, id, spec)
}
func verifyProductionLaunch(ctx context.Context, cfg Config, spec domain.LaunchSpec) error {
	if err := verifyManagedLaunch(ctx, cfg, spec); err != nil {
		return err
	}
	return verifyProcessIsolation(cfg)
}
