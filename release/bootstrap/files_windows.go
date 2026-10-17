package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func localPath(p string) bool {
	if len(p) < 3 || p[1] != ':' || strings.Contains(p[2:], ":") || strings.HasPrefix(p, `\\`) {
		return false
	}
	for _, part := range strings.Split(p[3:], string(filepath.Separator)) {
		if part != "" && (!filepath.IsLocal(part) || strings.TrimRight(part, ". ") != part) {
			return false
		}
	}
	return true
}
func unsafeInfo(i os.FileInfo) bool {
	if i.Mode()&os.ModeSymlink != 0 {
		return true
	}
	s, ok := i.Sys().(*syscall.Win32FileAttributeData)
	return !ok || s.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
func singleFile(f *os.File, _ os.FileInfo) bool {
	var info windows.ByHandleFileInformation
	return windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info) == nil && info.NumberOfLinks == 1 && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0
}

// Files inherit only from the protected private destination DACL. Unix modes
// describe the authenticated bundle contract and are rechecked during extraction.
func exactMode(_ *os.File, _ os.FileMode) error { return nil }

func privateDirectory(parent *os.Root, name string) (os.FileInfo, error) {
	token, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + token.User.Sid.String() + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		return nil, err
	}
	p, err := parent.Open(".")
	if err != nil {
		return nil, err
	}
	defer p.Close()
	n, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	oa := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(p.Fd()), ObjectName: n, Attributes: windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE, &oa, &status, nil, windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), name)
	defer f.Close()
	i, err := f.Stat()
	if err != nil {
		return nil, err
	}
	now, err := parent.Lstat(name)
	if err != nil || unsafeInfo(now) || !os.SameFile(i, now) {
		return nil, errors.New("private directory binding changed")
	}
	return i, nil
}
