//go:build linux

package launcher

import "path/filepath"

func equalExecutable(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }
