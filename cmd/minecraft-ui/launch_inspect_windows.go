//go:build windows

package main

import (
	"context"
	"fmt"
	"io"
)

func runNativeInspect(context.Context, []string, io.Writer) error {
	return fmt.Errorf("native-inspect is available only through the Linux privileged broker")
}
