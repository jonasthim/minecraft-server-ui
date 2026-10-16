//go:build !linux && !windows

package world

import "os"

// Non-release platforms retain rooted access and caller identity checks.
func openRead(root *os.Root, name string, directory bool) (*os.File, error) { return root.Open(name) }
