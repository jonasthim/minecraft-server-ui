//go:build windows

package managerstate

import (
	"errors"
	"os"
	"path/filepath"
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
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return "", errors.New("manager data DACL is unavailable")
	}
	var accountSID string
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		var raw *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &raw); err != nil || raw == nil {
			return "", errors.New("manager data DACL is malformed")
		}
		if raw.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		ace, sid, err := privateAllowACE(acl, index)
		if err != nil || !sid.IsValid() {
			return "", errors.New("manager data DACL is malformed")
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		account := sid.String()
		if !strings.HasPrefix(account, "S-1-5-21-") || !maskGrantsWrite(uint32(ace.Mask)) {
			continue
		}
		if accountSID != "" && accountSID != account {
			return "", errors.New("manager data DACL has multiple writable account principals")
		}
		accountSID = account
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

func maskGrantsWrite(mask uint32) bool {
	// FILE_DELETE_CHILD is a directory access right (0x40) that is not
	// currently exported by x/sys/windows. It permits deleting directory entries.
	const fileDeleteChild = uint32(0x00000040)
	const writeRights = uint32(windows.FILE_WRITE_DATA|windows.FILE_APPEND_DATA|windows.FILE_WRITE_EA|windows.FILE_WRITE_ATTRIBUTES|windows.DELETE|windows.WRITE_DAC|windows.WRITE_OWNER) | fileDeleteChild
	return mask&(windows.GENERIC_WRITE|windows.GENERIC_ALL|writeRights) != 0
}

// privateAllowACE validates the minimum ACE layout before interpreting the SID
// bytes. GetAce returns a pointer into the Windows-owned ACL buffer.
func privateAllowACE(acl *windows.ACL, index uint32) (*windows.ACCESS_ALLOWED_ACE, *windows.SID, error) {
	if acl == nil || index >= uint32(acl.AceCount) {
		return nil, nil, errors.New("ACL entry is unavailable")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, index, &ace); err != nil || ace == nil {
		return nil, nil, errors.New("ACL entry is unavailable")
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return nil, nil, errors.New("ACL entry is not an allow ACE")
	}
	if ace.Header.AceSize < uint16(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart)+8) {
		return nil, nil, errors.New("ACL entry is truncated")
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	// x/sys's Win32 pointer-returning count accessor loses Go allocation
	// provenance under checkptr. The minimum SID header was bounded above;
	// read its count byte directly, as the native updater ACL validator does.
	count := *(*byte)(unsafe.Add(unsafe.Pointer(sid), 1))
	sidBytes := 8 + 4*int(count)
	if count > 15 || sidBytes > int(ace.Header.AceSize)-int(unsafe.Offsetof(windows.ACCESS_ALLOWED_ACE{}.SidStart)) || !sid.IsValid() {
		return nil, nil, errors.New("ACL SID is truncated")
	}
	return ace, sid, nil
}

func checkExactPrivateDACL(sd *windows.SECURITY_DESCRIPTOR, managerSID string) error {
	if sd == nil {
		return errors.New("directory DACL is missing")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("directory DACL is not protected")
	}
	acl, defaulted, err := sd.DACL()
	if err != nil || defaulted || acl == nil || acl.AceCount != 3 {
		return errors.New("directory DACL is not the exact private allowlist")
	}
	want := map[string]bool{managerSID: false, "S-1-5-32-544": false, "S-1-5-18": false}
	for index := uint32(0); index < uint32(acl.AceCount); index++ {
		ace, sid, err := privateAllowACE(acl, index)
		if err != nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE || uint32(ace.Mask) != 0x001f01ff {
			return errors.New("directory DACL is not the exact private allowlist")
		}
		key := sid.String()
		seen, ok := want[key]
		if !ok || seen {
			return errors.New("directory DACL is not the exact private allowlist")
		}
		want[key] = true
	}
	for _, seen := range want {
		if !seen {
			return errors.New("directory DACL is not the exact private allowlist")
		}
	}
	return nil
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
	if err := checkExactPrivateDACL(sd, managerSID); err != nil {
		return err
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
