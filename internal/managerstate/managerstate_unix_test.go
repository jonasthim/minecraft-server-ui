//go:build !windows

package managerstate

import "testing"

func prepareManagerStateTestDirectory(*testing.T, string) {}

func testManagerStateSID(*testing.T) string { return "" }
