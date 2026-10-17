//go:build !windows

package managerstate

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func platformManagerSID(_ *os.Root) (string, error) { return "", nil }

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
	if err := parent.Mkdir(name, 0700); err != nil {
		return nil, nil, err
	}
	info, err := parent.Lstat(name)
	if err != nil || checkPrivateDirectory(parent, name, info, managerSID) != nil {
		return nil, nil, errors.New("created private directory is unsafe")
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
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("directory mode or type is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("directory owner is unsafe")
	}
	_ = parent
	_ = name
	_ = managerSID
	return nil
}

func checkOrdinaryFile(root *os.Root, name string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("file type is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return errors.New("file identity or permissions are unsafe")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("file identity changed")
	}
	return nil
}

func checkOpenFile(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("file type is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() || info.Mode().Perm()&0077 != 0 {
		return errors.New("file identity or permissions are unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("open file identity changed")
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
