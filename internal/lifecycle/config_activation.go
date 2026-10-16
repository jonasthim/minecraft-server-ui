package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

type configActivation struct {
	service *Service
	id      string
	next    installation
}

// PrepareConfigActivation runs under the existing stopped idle boundary before
// instance.Update opens its SQL transaction. Intent is durable before the boot
// blocker is published, so every blocker has a recovery journal even on crash.
func (s *Service) PrepareConfigActivation(ctx context.Context, id string, memoryMiB int) (instance.ConfigActivation, error) {
	current, err := s.loadInstallation(ctx, id)
	if err != nil {
		return nil, err
	}
	base, err := stripBoundMemory(current.Spec)
	if err != nil {
		return nil, err
	}
	current.Spec, err = bindMemory(base, memoryMiB)
	if err != nil {
		return nil, err
	}
	current.Generation, err = key()
	if err != nil {
		return nil, err
	}
	current.InitialLoader = false
	current.OriginalRevision = ""
	if err = s.intent(ctx, id, current); err != nil {
		return nil, err
	}
	if err = launcher.MarkActivationPending(ctx, s.dataDir, id); err != nil {
		return nil, err
	}
	return &configActivation{service: s, id: id, next: current}, nil
}
func (a *configActivation) Apply(ctx context.Context, tx *sql.Tx) error {
	raw, err := json.Marshal(a.next)
	if err != nil {
		return err
	}
	if err = a.service.seal(ctx, a.id, a.next.Spec); err != nil {
		return err
	}
	return saveInstallationTx(ctx, tx, a.id, a.next, raw)
}
func (a *configActivation) Finish(ctx context.Context) error {
	return a.service.recoverActivation(ctx, a.id)
}
