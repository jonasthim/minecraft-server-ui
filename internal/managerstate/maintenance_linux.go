//go:build linux

package managerstate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func authorizeMaintenance() error { return nil }

func runningImageHash(ctx context.Context) (string, error) {
	return runningImageDigest(ctx, "/proc/self/exe")
}

func fenceMaintenance(ctx context.Context, data, maintenance *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if data == nil || maintenance == nil {
		return errors.New("maintenance fence roots are unavailable")
	}
	dataPath := data.Name()
	if err := verifyDataBinding(ctx, dataPath, data); err != nil {
		return errors.New("manager data binding changed before durability fence")
	}
	maintenanceInfo, err := data.Lstat("maintenance")
	maintenanceOpened, openErr := maintenance.Stat(".")
	maintenancePath, pathErr := os.Lstat(filepath.Join(dataPath, "maintenance"))
	if err != nil || openErr != nil || pathErr != nil ||
		!os.SameFile(maintenanceInfo, maintenanceOpened) ||
		!os.SameFile(maintenanceInfo, maintenancePath) {
		return errors.New("maintenance directory binding changed before durability fence")
	}
	if err := syncDirectory(data); err != nil {
		return errors.New("manager data directory could not be synchronized")
	}
	if err := syncDirectory(maintenance); err != nil {
		return errors.New("maintenance directory could not be synchronized")
	}
	return ctx.Err()
}
