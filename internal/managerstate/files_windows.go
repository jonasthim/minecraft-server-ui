//go:build windows

package managerstate

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func platformManagerSID(data *os.Root) (string, error) {
	before, err := data.Stat(".")
	if err != nil {
		return "", err
	}
	sd, err := windows.GetNamedSecurityInfo(data.Name(), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return "", err
	}
	after, err := data.Stat(".")
	current, pathErr := os.Lstat(data.Name())
	if err != nil || pathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, current) {
		return "", errors.New("manager data directory identity changed")
	}
	text := sd.String()
	start := strings.Index(text, "D:")
	if start < 0 {
		return "", errors.New("manager data DACL is unavailable")
	}
	accounts := map[string]uint32{}
	for rest := text[start:]; ; {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			break
		}
		rest = rest[open+1:]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return "", errors.New("manager data DACL is malformed")
		}
		fields := strings.Split(rest[:end], ";")
		if len(fields) == 6 && fields[0] == "A" && strings.HasPrefix(fields[5], "S-1-5-21-") {
			accounts[fields[5]] |= parseSDDLMask(fields[2])
		}
		rest = rest[end+1:]
	}
	var accountSID string
	for sid, rights := range accounts {
		if rights&0x00000002 == 0 && rights&0x40000000 == 0 && rights != ^uint32(0) {
			continue
		}
		if accountSID != "" && accountSID != sid {
			return "", errors.New("manager data DACL has multiple writable account principals")
		}
		accountSID = sid
	}
	if accountSID == "" {
		return "", errors.New("manager account ACE is missing")
	}
	sid, err := windows.StringToSid(accountSID)
	if err != nil || sid == nil || !sid.IsValid() || sid.String() != accountSID {
		return "", errors.New("manager account SID is invalid")
	}
	return accountSID, nil
}

func parseSDDLMask(value string) uint32 {
	switch strings.ToUpper(value) {
	case "FA", "GA":
		return ^uint32(0)
	case "M":
		return 0x1301bf
	case "GW":
		return 0x40000000
	}
	if strings.HasPrefix(strings.ToLower(value), "0x") {
		parsed, err := strconv.ParseUint(value[2:], 16, 32)
		if err == nil {
			return uint32(parsed)
		}
	}
	return 0
}

func ensurePrivateDirectory(parent *os.Root, name, managerSID string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		root, _, createErr := createPrivateDirectory(parent, name, managerSID)
		return root, createErr
	}
	if err != nil || checkPrivateDirectory(parent, name, info, managerSID) != nil {
		return nil, errors.New("private directory is unsafe")
	}
	return openCheckedDirectory(parent, name, managerSID)
}

func openPrivateDirectory(parent *os.Root, name, managerSID string) (*os.Root, error) {
	return openCheckedDirectory(parent, name, managerSID)
}

func createPrivateDirectory(parent *os.Root, name, managerSID string) (*os.Root, os.FileInfo, error) {
	if !filepath.IsLocal(name) || name == "." || name == ".." {
		return nil, nil, errors.New("invalid private directory name")
	}
	sid, err := windows.StringToSid(managerSID)
	if err != nil || sid == nil || sid.String() != managerSID || !strings.HasPrefix(managerSID, "S-1-5-21-") {
		return nil, nil, errors.New("manager account SID is invalid")
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)")
	if err != nil {
		return nil, nil, err
	}
	parentFile, err := parent.Open(".")
	if err != nil {
		return nil, nil, err
	}
	defer parentFile.Close()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parentFile.Fd()), ObjectName: objectName, Attributes: windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil, windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return nil, nil, err
	}
	created := os.NewFile(uintptr(handle), name)
	defer created.Close()
	info, err := created.Stat()
	if err != nil {
		return nil, nil, err
	}
	if err := checkPrivateDirectory(parent, name, info, managerSID); err != nil {
		return nil, nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, nil, errors.New("private directory identity changed")
	}
	return root, info, nil
}

func openCheckedDirectory(parent *os.Root, name, managerSID string) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil || checkPrivateDirectory(parent, name, info, managerSID) != nil {
		return nil, errors.New("private directory is unsafe")
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	current, statErr := parent.Lstat(name)
	if err != nil || statErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		_ = root.Close()
		return nil, errors.New("private directory identity changed")
	}
	return root, nil
}

func checkPrivateDirectory(parent *os.Root, name string, info os.FileInfo, managerSID string) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("directory type is unsafe")
	}
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("directory reparse point is unsafe")
	}
	child, err := parent.Open(name)
	if err != nil {
		return err
	}
	defer child.Close()
	opened, err := child.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("directory identity changed")
	}
	sd, err := windows.GetNamedSecurityInfo(filepath.Join(parent.Name(), name), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || !privateDirectoryOwnerAllowed(owner, managerSID) {
		return errors.New("directory owner is unsafe")
	}
	actual := sd.String()
	aclStart := strings.Index(actual, "D:")
	if aclStart < 0 {
		return errors.New("directory DACL is missing")
	}
	expected := "D:P(A;OICI;FA;;;" + managerSID + ")(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"
	if actual[aclStart:] != expected {
		return errors.New("directory DACL is not the exact private allowlist")
	}
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("directory path identity changed")
	}
	return nil
}

func privateDirectoryOwnerAllowed(owner *windows.SID, managerSID string) bool {
	if owner == nil {
		return false
	}
	if owner.String() == managerSID {
		return true
	}
	return owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) || owner.IsWellKnown(windows.WinLocalSystemSid)
}

func checkOrdinaryFile(root *os.Root, name string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("file type is unsafe")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := checkOpenFile(f, info); err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("file identity changed")
	}
	return nil
}

func checkOpenFile(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("file type is unsafe")
	}
	var attributes windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &attributes); err != nil || attributes.NumberOfLinks != 1 || attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("file identity or reparse state is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("open file identity changed")
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	// Windows Go does not expose a portable directory metadata flush. Every
	// snapshot file is flushed individually before the directory is reported;
	// callers must not infer power-loss durability for the parent entry here.
	_ = root
	return nil
}
