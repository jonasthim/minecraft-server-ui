//go:build windows

package installinputs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const privateDACL = "D:P(A;OICI;FA;;;BA)(A;OICI;FA;;;SY)"

func validateTransactionRoot(root *os.Root, testRoot bool) error {
	if root == nil {
		return errors.New("transaction root is unavailable")
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() {
		return errors.New("transaction root is unsafe")
	}
	if err := verifyDirectorySecurity(root.Name(), privateDACL, testRoot); err != nil {
		return err
	}
	return verifyRootBinding(root.Name(), root)
}

func validateRecordedInputsDirectory(root *os.Root, info os.FileInfo, _ bool) error {
	if root == nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recorded inputs directory is unsafe")
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("recorded inputs directory identity changed")
	}
	if err := verifyDirectorySecurity(root.Name(), privateDACL, false); err != nil {
		return errors.New("recorded inputs directory DACL is unsafe")
	}
	return nil
}

func validateRecordedInputMetadata(file *os.File, info os.FileInfo, _ os.FileMode, _ bool) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recorded input file type is unsafe")
	}
	if file != nil {
		return checkOpenFile(file, info)
	}
	return nil
}

func validateOpenedMember(file *os.File, info os.FileInfo, _ os.FileMode) error {
	return checkOpenFile(file, info)
}

func checkSourcePath(root *os.Root, name string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepared source is not ordinary")
	}
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok || attrs.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("prepared source reparse point is unsafe")
	}
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkSourceOpen(f, info)
}

func checkSourceOpen(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("prepared source is not regular")
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &data); err != nil || data.NumberOfLinks != 1 || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("prepared source link identity is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("prepared source handle changed")
	}
	return nil
}

func createInputsDirectory(parent *os.Root, _ bool) (*os.Root, os.FileInfo, error) {
	created, err := createDirectoryHandle(parent, inputsName)
	if err != nil {
		if created {
			return nil, nil, errors.Join(errInputsDirectoryCreated, err)
		}
		return nil, nil, err
	}
	info, err := parent.Lstat(inputsName)
	if err != nil {
		return nil, nil, errors.Join(errInputsDirectoryCreated, err)
	}
	if !info.IsDir() {
		return nil, info, errors.Join(errInputsDirectoryCreated, errors.New("new private directory is unsafe"))
	}
	root, err := parent.OpenRoot(inputsName)
	if err != nil {
		return nil, info, errors.Join(errInputsDirectoryCreated, err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, info, errors.Join(errInputsDirectoryCreated, errors.New("new private directory identity changed"), err)
	}
	if err := verifyDirectorySecurity(filepath.Join(parent.Name(), inputsName), privateDACL, false); err != nil {
		return root, info, errors.Join(errInputsDirectoryCreated, errors.New("new private directory DACL changed"), err)
	}
	return root, info, nil
}

func createDirectoryHandle(parent *os.Root, name string) (bool, error) {
	descriptor, err := windows.SecurityDescriptorFromString("O:BA" + privateDACL)
	if err != nil {
		return false, err
	}
	parentFile, err := parent.Open(".")
	if err != nil {
		return false, err
	}
	defer parentFile.Close()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return false, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parentFile.Fd()), ObjectName: objectName, Attributes: windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE, &attributes, &status, nil, windows.FILE_ATTRIBUTE_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT, 0, 0)
	if err != nil {
		return false, err
	}
	return true, windows.CloseHandle(handle)
}

func createOutput(root *os.Root, name string, mode os.FileMode, owned *map[string]ownedFile) (*os.File, error) {
	if !filepath.IsLocal(name) || name == "." || name == ".." {
		return nil, errors.New("output name is unsafe")
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || checkOpenFile(file, info) != nil {
		_ = file.Close()
		return nil, errors.New("new output file is unsafe")
	}
	(*owned)[name] = ownedFile{name: name, info: info, pin: file, mode: mode}
	if err := setOutputMode(file, mode); err != nil {
		return nil, err
	}
	return file, nil
}

func setOutputMode(_ *os.File, _ os.FileMode) error { return nil }

func checkOpenFile(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("output file is not regular")
	}
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &data); err != nil || data.NumberOfLinks != 1 || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("output file identity is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("output handle changed")
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	text := sd.String()
	index := strings.Index(text, "D:")
	if index < 0 || !onlyAdminSystemFullAccess(text[index:]) {
		return errors.New("output file DACL is not private")
	}
	return nil
}

func onlyAdminSystemFullAccess(dacl string) bool {
	if !strings.HasPrefix(dacl, "D:") {
		return false
	}
	seen := map[string]bool{}
	for rest := dacl[2:]; rest != ""; {
		if rest[0] != '(' {
			return false
		}
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return false
		}
		fields := strings.Split(rest[1:end], ";")
		if len(fields) != 6 || fields[0] != "A" || (fields[2] != "FA" && fields[2] != "GA") || (fields[5] != "BA" && fields[5] != "SY") || seen[fields[5]] {
			return false
		}
		seen[fields[5]] = true
		rest = rest[end+1:]
	}
	return seen["BA"] && seen["SY"]
}

func verifyDirectorySecurity(path, expected string, allowCurrentOwner bool) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errors.New("directory owner is unavailable")
	}
	if allowCurrentOwner {
		token, tokenErr := windows.OpenCurrentProcessToken()
		if tokenErr != nil {
			return tokenErr
		}
		user, userErr := token.GetTokenUser()
		_ = token.Close()
		if userErr != nil || user == nil || user.User.Sid == nil || !owner.Equals(user.User.Sid) {
			return errors.New("test directory owner is not the current account")
		}
	} else if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) && !owner.IsWellKnown(windows.WinLocalSystemSid) {
		return errors.New("directory owner is unsafe")
	}
	text := sd.String()
	index := strings.Index(text, "D:")
	if index < 0 || text[index:] != expected {
		return errors.New("directory DACL is not exact and protected")
	}
	return nil
}

func syncDirectory(_ *os.Root) error { return nil }
