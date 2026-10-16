package backup

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"io/fs"
	"os"
)

// A backup restore must never race forward data replacement against a separate
// configuration/executable recovery. In particular, later materialization must
// not recover an older config preimage over newly restored ordinary properties.
func (s *Service) restorePreconditions(ctx context.Context, dirs instance.Directories, id string) error {
	state, err := openSource(dirs.State)
	if err != nil {
		return err
	}
	defer state.Close()
	_, err = state.root.Lstat("config.pending.json")
	if err == nil {
		return domain.E(domain.CodeUnavailable, "configuration recovery must complete before backup restore")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err = state.verify(); err != nil {
		return err
	}
	var pending bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM lifecycle_activations WHERE instance_id=?)`, id).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return domain.E(domain.CodeUnavailable, "server activation recovery must complete before backup restore")
	}
	server, err := openSource(dirs.Server)
	if err != nil {
		return err
	}
	defer server.Close()
	info, err := server.root.Lstat(".manager")
	if errors.Is(err, fs.ErrNotExist) {
		return server.verify()
	}
	if err != nil {
		return err
	}
	if !safeInfo(info) || !info.IsDir() {
		return errCorrupt
	}
	manager, err := server.root.OpenRoot(".manager")
	if err != nil {
		return err
	}
	defer manager.Close()
	opened, err := manager.Stat(".")
	if err != nil {
		return err
	}
	current, err := server.root.Lstat(".manager")
	if err != nil {
		return err
	}
	if !safeInfo(current) || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		return errStale
	}
	_, err = manager.Lstat("activation.pending.json")
	if err == nil {
		return domain.E(domain.CodeUnavailable, "server activation marker must be reconciled before backup restore")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return server.verify()
}
