//go:build !windows

package managerstate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func restoreNativeIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("restore file handle is unavailable")
	}
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return "", errors.New("native file identity is unavailable")
	}
	return fmt.Sprintf("unix:%d:%d", uint64(stat.Dev), uint64(stat.Ino)), nil
}

func validRestoreNativeIdentity(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "unix" {
		return false
	}
	device, errDevice := strconv.ParseUint(parts[1], 10, 64)
	inode, errInode := strconv.ParseUint(parts[2], 10, 64)
	return errDevice == nil && errInode == nil && inode != 0 && strconv.FormatUint(device, 10) == parts[1] && strconv.FormatUint(inode, 10) == parts[2]
}
