//go:build linux

package mapdata

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

func safeInfo(i fs.FileInfo) bool { return i != nil && i.Mode()&os.ModeSymlink == 0 }
func openRead(root *os.Root, name string) (*os.File, error) {
	f, e := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 {
		f.Close()
		return nil, errCorrupt
	}
	return f, nil
}
func fileIdentity(f *os.File) (string, error) {
	i, e := f.Stat()
	if e != nil {
		return "", e
	}
	v := i.Sys().(*syscall.Stat_t)
	if v.Nlink != 1 {
		return "", errCorrupt
	}
	return fmt.Sprintf("%d:%d", v.Dev, v.Ino), nil
}
