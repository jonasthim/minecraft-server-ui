//go:build !linux && !windows

package selfupdate

import (
	"context"
	"errors"
	"io"
	"os"
)

func platformPaths() nativePaths { return nativePaths{} }
func openProtectedRoot(context.Context, string) (*os.Root, error) {
	return nil, errors.New("native manager updates are unavailable on this platform")
}
func protectedFile(*os.Root, string) error {
	return errors.New("native manager updates are unavailable on this platform")
}
func brokerAvailable(context.Context, nativePaths) bool { return false }
func brokerRequest(context.Context, nativePaths, string) error {
	return errors.New("native manager updates are unavailable on this platform")
}
func RunNativeUpdater(context.Context, []string, io.Writer) error {
	return errors.New("native manager updates are unavailable on this platform")
}
