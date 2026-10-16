//go:build windows

package world

import (
	"golang.org/x/sys/windows"
	"os"
)

func openRead(root *os.Root, name string, directory bool) (*os.File, error) {
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	var info windows.ByHandleFileInformation
	if e = windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); e != nil {
		f.Close()
		return nil, e
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || !directory && info.NumberOfLinks > 1 {
		f.Close()
		return nil, unsafe()
	}
	return f, nil
}
