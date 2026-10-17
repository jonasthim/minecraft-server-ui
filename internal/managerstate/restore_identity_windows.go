//go:build windows

package managerstate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

func restoreNativeIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("restore file handle is unavailable")
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	index := uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)
	if index == 0 {
		return "", errors.New("native file identity is unavailable")
	}
	return fmt.Sprintf("windows:%08x:%016x", info.VolumeSerialNumber, index), nil
}

func validRestoreNativeIdentity(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 3 || parts[0] != "windows" || len(parts[1]) != 8 || len(parts[2]) != 16 {
		return false
	}
	_, errVolume := strconv.ParseUint(parts[1], 16, 32)
	index, errIndex := strconv.ParseUint(parts[2], 16, 64)
	return errVolume == nil && errIndex == nil && index != 0 && strings.ToLower(parts[1]) == parts[1] && strings.ToLower(parts[2]) == parts[2]
}
