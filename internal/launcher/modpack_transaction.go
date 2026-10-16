package launcher

import (
	"context"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io/fs"
)

// checkModpackTransactionReady is independent of manager SQL and is checked
// before a launcher can run, including systemd boot before manager recovery.
// Any marker presence, malformed marker or unsafe parent fails closed.
func checkModpackTransactionReady(ctx context.Context, dataDir, id string) error {
	paths, e := Paths(dataDir, id)
	if e != nil {
		return e
	}
	root, e := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(e, fs.ErrNotExist) {
		return nil
	}
	if e != nil {
		return fmt.Errorf("open modpack transaction blocker directory: %w", e)
	}
	defer root.Close()
	_, e = root.Lstat("modpack-transaction.json")
	if e == nil {
		return fmt.Errorf("modpack transaction requires manager reconciliation")
	}
	if !errors.Is(e, fs.ErrNotExist) {
		return fmt.Errorf("inspect modpack transaction blocker: %w", e)
	}
	return ctx.Err()
}
