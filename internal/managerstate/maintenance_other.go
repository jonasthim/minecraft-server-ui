//go:build !linux && !windows

package managerstate

import (
	"context"
	"errors"
	"os"
)

func authorizeMaintenance() error {
	return errors.New("offline migration is unavailable on this platform")
}
func runningImageHash(context.Context) (string, error) {
	return "", errors.New("running manager image cannot be verified on this platform")
}
func fenceMaintenance(context.Context, *os.Root, *os.Root) error {
	return errors.New("maintenance durability fence is unavailable on this platform")
}
