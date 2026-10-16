//go:build windows

package backup

import (
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

// The NT rename uses opened parent directory handles and a relative basename.
// FILE_OPEN_REPARSE_POINT prevents traversal through the renamed leaf.
func renameBetween(from *os.Root, name string, to *os.Root, dest string) error {
	if safePath(name) != nil || safePath(dest) != nil {
		return errCorrupt
	}
	a, err := from.Open(".")
	if err != nil {
		return err
	}
	defer a.Close()
	b, err := to.Open(".")
	if err != nil {
		return err
	}
	defer b.Close()
	object, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(a.Fd()), ObjectName: object}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.DELETE|windows.FILE_READ_ATTRIBUTES, &attrs, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_OPEN_FOR_BACKUP_INTENT, 0, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errCorrupt
	}
	utf, err := windows.UTF16FromString(dest)
	if err != nil {
		return err
	}
	type renameInfo struct {
		Flags  uint32
		Root   windows.Handle
		Length uint32
		Name   [1]uint16
	}
	size := int(unsafe.Offsetof(renameInfo{}.Name)) + (len(utf)-1)*2
	buffer := make([]byte, size)
	entry := (*renameInfo)(unsafe.Pointer(&buffer[0]))
	entry.Root = windows.Handle(b.Fd())
	entry.Length = uint32((len(utf) - 1) * 2)
	copy(unsafe.Slice(&entry.Name[0], len(utf)-1), utf[:len(utf)-1])
	return windows.NtSetInformationFile(h, &status, &buffer[0], uint32(size), windows.FileRenameInformation)
}
