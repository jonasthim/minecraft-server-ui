//go:build !windows

package installinputs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func validateTransactionRoot(root *os.Root, testRoot bool) error {
	if root == nil {
		return errors.New("transaction root is unavailable")
	}
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("transaction root permissions are unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("transaction root owner is unavailable")
	}
	expected := uint32(0)
	if testRoot {
		expected = uint32(os.Geteuid())
	}
	if stat.Uid != expected {
		return errors.New("transaction root owner is unsafe")
	}
	return verifyRootBinding(root.Name(), root)
}

func checkSourcePath(root *os.Root, name string, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepared source is not an ordinary file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("prepared source link count is unsafe")
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return errors.New("prepared source path changed")
	}
	return nil
}

func checkSourceOpen(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("prepared source is not regular")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("prepared source link count is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("prepared source handle changed")
	}
	return nil
}

func createInputsDirectory(parent *os.Root, testRoot bool) (*os.Root, os.FileInfo, error) {
	if err := parent.Mkdir(inputsName, 0700); err != nil {
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
	if info.Mode().Perm()&0077 != 0 {
		return root, info, errors.Join(errInputsDirectoryCreated, errors.New("new private directory is unsafe"))
	}
	return root, info, nil
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
	(*owned)[name] = ownedFile{name: name, info: info, pin: file}
	if err := setOutputMode(file, mode); err != nil {
		return nil, err
	}
	return file, nil
}

func setOutputMode(file *os.File, mode os.FileMode) error { return file.Chmod(mode.Perm()) }

func checkOpenFile(file *os.File, info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("output file is not regular")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 || int(stat.Uid) != os.Geteuid() {
		return errors.New("output file identity is unsafe")
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("output handle identity changed")
	}
	return nil
}

func syncDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
