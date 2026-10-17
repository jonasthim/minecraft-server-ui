//go:build windows

package managerstate

import "os"

// The caller flushes and revalidates the entire fixed local C: NTFS volume
// through fenceMaintenance after this step; Windows directory handles are not fsynced.
func syncRestoreDirectories(_ func(*os.Root) error, _, _, _ *os.Root) error { return nil }

func syncRestoreMoveDirectories(_ func(*os.Root) error, _, _, _ *os.Root) error { return nil }
