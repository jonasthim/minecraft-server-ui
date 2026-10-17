//go:build !windows

package bootstrap

import (
	"errors"
	"os"
	"syscall"
)

func localPath(string) bool         { return true }
func unsafeInfo(i os.FileInfo) bool { return i.Mode()&os.ModeSymlink != 0 }
func singleFile(_ *os.File, i os.FileInfo) bool {
	s, ok := i.Sys().(*syscall.Stat_t)
	return ok && s.Nlink == 1
}
func duplicateCreationFile(f *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(f.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	pinned := os.NewFile(uintptr(fd), "bootstrap-owned-file")
	if pinned == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("could not retain created file handle")
	}
	return pinned, nil
}
func exactMode(f *os.File, m os.FileMode) error { return f.Chmod(m) }
func privateDirectory(parent *os.Root, name string) (os.FileInfo, error) {
	if err := parent.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	return parent.Lstat(name)
}
