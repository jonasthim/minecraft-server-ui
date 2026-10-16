//go:build !windows

package selfupdate

import (
	"context"
	"errors"
	"os"
)

func syncRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func syncPrivilegedRoot(ctx context.Context, root *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return syncRoot(root)
}
