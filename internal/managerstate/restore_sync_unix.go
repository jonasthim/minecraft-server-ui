//go:build !windows

package managerstate

import "os"

func syncRestoreDirectories(syncDir func(*os.Root) error, stage, preimage, operation *os.Root) error {
	if syncDir == nil {
		syncDir = syncDirectory
	}
	if err := syncDir(stage); err != nil {
		return err
	}
	if err := syncDir(preimage); err != nil {
		return err
	}
	return syncDir(operation)
}

func syncRestoreMoveDirectories(syncDir func(*os.Root) error, destination, source, operation *os.Root) error {
	if syncDir == nil {
		syncDir = syncDirectory
	}
	if err := syncDir(destination); err != nil {
		return err
	}
	if err := syncDir(source); err != nil {
		return err
	}
	return syncDir(operation)
}
