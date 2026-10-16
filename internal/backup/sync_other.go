//go:build !linux

package backup

import "os"

// Native Windows directory durability still requires real qualification.
func syncDir(root *os.Root, name string) error { return nil }
