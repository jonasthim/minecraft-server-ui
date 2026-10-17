package main

import (
	"context"
	"errors"
	"io"
)

func inspectUpdaterProcessAccess(context.Context, io.Writer) error {
	return errors.New("updater process access qualification requires Windows")
}
