//go:build !linux

package instance

import "os"

// Portable file fsync/rename is used; native Windows durability qualification
// remains required before power-loss guarantees can be claimed there.
func syncDirectory(_ *os.Root, _ string) error { return nil }
