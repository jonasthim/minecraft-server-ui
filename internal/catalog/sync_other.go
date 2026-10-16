//go:build !linux

package catalog

// No native Windows qualification is claimed by the Linux package tests.
func syncArtifactDirectory(string) error { return nil }
