//go:build linux

package backup

import (
	"golang.org/x/sys/unix"
	"os"
)

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
	return unix.Renameat2(int(a.Fd()), name, int(b.Fd()), dest, unix.RENAME_NOREPLACE)
}
