//go:build !windows

package selfupdate

import (
	"errors"
	"os"
	"syscall"
)

func fileLinkCount(file *os.File) (uint32, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Nlink) > uint64(^uint32(0)) {
		return 0, errors.New("maintenance receipt link count is unavailable")
	}
	return uint32(stat.Nlink), nil
}
