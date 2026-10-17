//go:build windows

package selfupdate

import (
	"os"

	"golang.org/x/sys/windows"
)

func fileLinkCount(file *os.File) (uint32, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return info.NumberOfLinks, nil
}
