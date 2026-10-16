//go:build !linux && !windows

package mapdata

import (
	"io/fs"
	"os"
)

func safeInfo(i fs.FileInfo) bool { return i != nil && i.Mode()&os.ModeSymlink == 0 }

// Unqualified host filesystems are refused rather than promising link safety.
func openRead(root *os.Root, name string) (*os.File, error) { return nil, errUnsupported }
func fileIdentity(f *os.File) (string, error)               { return "", errUnsupported }
