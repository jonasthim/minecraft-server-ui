package launcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// Any backup restore blocker presence prevents independent launcher boot.
// OpenRoot anchors every local filesystem component and rejects links/reparse
// ancestors. Only an absent leaf marker permits launch; host errors stay errors.
func checkBackupRestoreReady(ctx context.Context, dataDir, id string) error {
	paths, err := Paths(dataDir, id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open restore marker directory: %w", err)
	}
	defer root.Close()
	_, err = root.Lstat("backup-restore.json")
	if err == nil {
		return fmt.Errorf("backup restore is incomplete; reconcile before launching")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect backup restore blocker: %w", err)
	}
	return ctx.Err()
}
