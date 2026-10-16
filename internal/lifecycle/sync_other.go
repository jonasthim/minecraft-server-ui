//go:build !linux

package lifecycle

import "os"

func syncDirectory(*os.Root) error { return nil }
