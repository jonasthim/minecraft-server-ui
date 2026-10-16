package content

import (
	"golang.org/x/sys/windows"
	"os"
)

func singleLink(f *os.File, _ os.FileInfo) error {
	var info windows.ByHandleFileInformation
	if e := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); e != nil {
		return e
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return invalid("linked content file refused")
	}
	return nil
}

// Windows rename durability remains subject to native qualification.
func syncDir(_ *os.Root) error { return nil }
