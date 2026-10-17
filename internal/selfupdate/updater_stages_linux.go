//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ensureLinuxUpdaterStagesRoot creates the one fixed protected stage parent
// required by the privileged updater. It is called only after RunNativeUpdater
// has acquired the updater lock. Existing directories are validated in place;
// their contents and metadata are never repaired or removed.
func ensureLinuxUpdaterStagesRoot(ctx context.Context, state *os.Root, statePath string) (*os.Root, error) {
	return ensureLinuxUpdaterStagesRootWithOwner(ctx, state, statePath, 0, 0)
}

func ensureLinuxUpdaterStagesRootWithOwner(ctx context.Context, state *os.Root, statePath string, ownerUID, ownerGID uint32) (*os.Root, error) {
	if ctx == nil || state == nil || !filepath.IsAbs(statePath) || filepath.Clean(statePath) != statePath {
		return nil, errors.New("invalid protected updater stages arguments")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := sameRoot(ctx, statePath, state); err != nil {
		return nil, fmt.Errorf("protected updater state binding changed: %w", err)
	}

	info, err := state.Lstat("stages")
	if errors.Is(err, os.ErrNotExist) {
		if err := state.Mkdir("stages", 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create protected updater stages directory: %w", err)
		}
		if err := syncPrivilegedRoot(ctx, state); err != nil {
			return nil, fmt.Errorf("sync protected updater state after stages creation: %w", err)
		}
		info, err = state.Lstat("stages")
	}
	if err != nil {
		return nil, fmt.Errorf("inspect protected updater stages directory: %w", err)
	}
	if err := validateLinuxUpdaterStagesInfo(info, ownerUID, ownerGID); err != nil {
		return nil, err
	}
	stages, err := state.OpenRoot("stages")
	if err != nil {
		return nil, fmt.Errorf("open protected updater stages directory: %w", err)
	}
	opened, err := stages.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = stages.Close()
		return nil, errors.Join(errors.New("protected updater stages directory changed while opening"), err)
	}
	if err := validateLinuxUpdaterStagesInfo(opened, ownerUID, ownerGID); err != nil {
		_ = stages.Close()
		return nil, err
	}
	if err := sameRoot(ctx, statePath, state); err != nil {
		_ = stages.Close()
		return nil, fmt.Errorf("protected updater state binding changed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = stages.Close()
		return nil, err
	}
	return stages, nil
}

func validateLinuxUpdaterStagesInfo(info os.FileInfo, ownerUID, ownerGID uint32) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 || stat.Uid != ownerUID || stat.Gid != ownerGID || stat.Ino == 0 {
		return errors.New("protected updater stages path is not an exact root-owned private directory")
	}
	return nil
}
