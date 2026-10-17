//go:build !windows

package bootstrap

import (
	"os"
	"syscall"
)

func localPath(string) bool         { return true }
func unsafeInfo(i os.FileInfo) bool { return i.Mode()&os.ModeSymlink != 0 }
func singleFile(_ *os.File, i os.FileInfo) bool {
	s, ok := i.Sys().(*syscall.Stat_t)
	return ok && s.Nlink == 1
}
func exactMode(f *os.File, m os.FileMode) error { return f.Chmod(m) }
func privateDirectory(parent *os.Root, name string) (os.FileInfo, error) {
	if err := parent.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	return parent.Lstat(name)
}
