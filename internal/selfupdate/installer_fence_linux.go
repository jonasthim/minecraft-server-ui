//go:build linux

package selfupdate

import (
	"context"
	"errors"
	"os"
)

// The installer and the ordinary updater share the same kernel lock. This
// presence fence also protects the handoff between preparation and the
// independent installer worker. Only the install package may interpret or
// recover these records; ordinary activation never removes them.
func refuseLinuxInstallerState(ctx context.Context, state *os.Root) error {
	if ctx == nil || state == nil {
		return errors.New("installer coordination state is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{"installation-transaction.json", "installation-transaction.pending", "installation-launch.json", "installation-launch.pending"} {
		if _, err := state.Lstat(name); err == nil {
			return errors.New("installer transaction requires completion or recovery")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("installer coordination state cannot be inspected")
		}
	}
	return ctx.Err()
}
