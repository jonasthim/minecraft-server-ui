//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
)

// Service-account staging/receipts are explicitly nondurable directory
// publications on Windows. The independent native importer reopens, re-verifies
// and volume-fences privileged copies before any activation can begin.
func syncRoot(*os.Root) error { return nil }

// syncPrivilegedRoot uses the documented administrative whole-volume fence,
// never directory-handle Sync. It accepts only fixed local installation roots
// on the fixed C: NTFS volume; unsupported/mounted/reparse layouts fail closed.
func syncPrivilegedRoot(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths := platformPaths()
	path := filepath.Clean(root.Name())
	allowed := false
	for _, base := range []string{paths.State, paths.Bin, controlDirectory(paths.Data)} {
		if strings.EqualFold(path, base) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(base)+string(filepath.Separator)) {
			allowed = true
		}
	}
	if !allowed || !strings.EqualFold(filepath.VolumeName(path), "C:") {
		return errors.New("native durability fence requires the fixed installation volume")
	}
	checked, err := safepath.OpenRoot(ctx, path)
	if err != nil {
		return err
	}
	defer checked.Close()
	before, err := root.Stat(".")
	if err != nil {
		return err
	}
	current, err := checked.Stat(".")
	if err != nil || !os.SameFile(before, current) {
		return errors.New("native publication directory changed")
	}
	fileName, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	volumePath := make([]uint16, 260)
	if err := windows.GetVolumePathName(fileName, &volumePath[0], uint32(len(volumePath))); err != nil {
		return err
	}
	if !strings.EqualFold(windows.UTF16ToString(volumePath), `C:\`) {
		return errors.New("native publication traverses an unexpected volume mount")
	}
	mount, _ := windows.UTF16PtrFromString(`C:\`)
	if windows.GetDriveType(mount) != windows.DRIVE_FIXED {
		return errors.New("native update volume must be a fixed local disk")
	}
	fs := make([]uint16, 64)
	var serial uint32
	if err := windows.GetVolumeInformation(mount, nil, 0, &serial, nil, nil, &fs[0], uint32(len(fs))); err != nil {
		return err
	}
	if windows.UTF16ToString(fs) != "NTFS" {
		return errors.New("native update durability currently requires NTFS")
	}
	volume, _ := windows.UTF16PtrFromString(`\\.\C:`)
	handle, err := windows.CreateFile(volume, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return errors.New("native update volume fence could not be opened")
	}
	defer windows.CloseHandle(handle)
	var openedSerial uint32
	openedFS := make([]uint16, 64)
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, &openedSerial, nil, nil, &openedFS[0], uint32(len(openedFS))); err != nil {
		return err
	}
	if serial != openedSerial || windows.UTF16ToString(openedFS) != "NTFS" {
		return errors.New("native update volume identity changed")
	}
	if err := windows.FlushFileBuffers(handle); err != nil {
		return errors.New("native update volume flush failed; publication is not durable")
	}
	if err := sameRoot(ctx, path, root); err != nil {
		return err
	}
	return ctx.Err()
}
