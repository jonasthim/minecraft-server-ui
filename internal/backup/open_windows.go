//go:build windows

package backup

import (
	"fmt"
	"golang.org/x/sys/windows"
	"io/fs"
	"os"
	"syscall"
)

func safeInfo(i fs.FileInfo) bool {
	if i == nil || i.Mode()&os.ModeSymlink != 0 {
		return false
	}
	v, ok := i.Sys().(*syscall.Win32FileAttributeData)
	return ok && v.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}
func windowsInfo(f *os.File) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	e := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info)
	return info, e
}
func openRead(root *os.Root, name string) (*os.File, error) {
	f, e := root.Open(name)
	if e != nil {
		return nil, e
	}
	info, e := windowsInfo(f)
	if e != nil {
		f.Close()
		return nil, e
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.NumberOfLinks != 1 {
		f.Close()
		return nil, errCorrupt
	}
	return f, nil
}
func fileIdentity(f *os.File) (string, error) {
	i, e := windowsInfo(f)
	if e != nil {
		return "", e
	}
	if i.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || i.NumberOfLinks != 1 {
		return "", errCorrupt
	}
	return fmt.Sprintf("%d:%d:%d", i.VolumeSerialNumber, i.FileIndexHigh, i.FileIndexLow), nil
}
