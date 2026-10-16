//go:build windows

package launcher

// Windows Job Objects enforce lifetime containment only. Filesystem/credential
// isolation has not yet been implemented or qualified on this platform.
func verifyProcessIsolation(Config) error { return nil }
