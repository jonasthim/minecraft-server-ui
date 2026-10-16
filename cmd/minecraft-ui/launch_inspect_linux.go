//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"io"
)

func runNativeInspect(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "--instance" {
		return fmt.Errorf("native-inspect accepts only --instance ID")
	}
	if err := domain.ValidateInstanceID(args[1]); err != nil {
		return err
	}
	inspection, err := supervisor.NativeInspect(ctx, args[1])
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(inspection)
}
