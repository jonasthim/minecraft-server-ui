//go:build linux

package world

import (
	"os"
	"syscall"
)

// Nonblocking opens cannot hang if a regular source is replaced with a FIFO.
// O_NOFOLLOW also rejects a final-component link before any content is read.
func openRead(root *os.Root, name string, directory bool) (*os.File, error) {
	flags := os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW
	if directory {
		flags |= syscall.O_DIRECTORY
	}
	f, e := root.OpenFile(name, flags, 0)
	if e != nil {
		return nil, e
	}
	info, e := f.Stat()
	if e != nil {
		f.Close()
		return nil, e
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && !directory && stat.Nlink > 1 {
		f.Close()
		return nil, unsafe()
	}
	return f, nil
}
