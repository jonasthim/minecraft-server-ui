package update

import (
	"context"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// Independent participants remain strict even when the coordinator owns its own
// marker. Recovery calls this before touching any update postimage.
func (s *Service) checkOrdinaryReady(ctx context.Context, id string) error {
	for _, check := range []func(context.Context, string) error{s.config.Backups.CheckReady, s.config.Content.CheckReady, s.config.Packs.CheckReady} {
		if err := check(ctx, id); err != nil {
			return err
		}
	}
	var pending bool
	if err := s.config.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM instance_mutations WHERE instance_id=?) OR EXISTS(SELECT 1 FROM lifecycle_activations WHERE instance_id=?)`, id, id).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("ordinary instance or launch mutation must finish before update")
	}
	data, err := s.dataDir(id)
	if err != nil {
		return err
	}
	pending, err = launcher.PendingConfig(data, id)
	if err != nil {
		return err
	}
	if pending {
		return conflict("ordinary configuration recovery must finish before update")
	}
	return nil
}
func (s *Service) checkAdmissionReady(ctx context.Context, id string) error {
	if err := s.CheckReady(ctx, id); err != nil {
		return err
	}
	if err := s.checkOrdinaryReady(ctx, id); err != nil {
		return err
	}
	data, err := s.dataDir(id)
	if err != nil {
		return err
	}
	return launcher.CheckActivationReady(ctx, data, id)
}
