package launcher

import (
	"context"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"io/fs"
)

// checkContentTransactionReady is independent of manager SQL and is checked
// before a launcher can run, including systemd boot before manager recovery.
// Any marker presence, malformed marker or unsafe parent fails closed.
func checkContentTransactionReady(ctx context.Context, dataDir, id string) error {
	paths, e := Paths(dataDir, id)
	if e != nil {
		return e
	}
	root, e := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(e, fs.ErrNotExist) {
		return nil
	}
	if e != nil {
		return fmt.Errorf("open content transaction blocker directory: %w", e)
	}
	defer root.Close()
	_, e = root.Lstat("content-transaction.json")
	if e == nil {
		return fmt.Errorf("content transaction requires manager reconciliation")
	}
	if !errors.Is(e, fs.ErrNotExist) {
		return fmt.Errorf("inspect content transaction blocker: %w", e)
	}
	return ctx.Err()
}
