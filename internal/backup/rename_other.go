//go:build !linux && !windows

package backup

import "os"

func renameBetween(from *os.Root, name string, to *os.Root, dest string) error { return errUnsupported }
