package update

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

type BeginStop func(context.Context, *jobs.Execution, domain.User) (int64, lifecycle.RecoveryStartGuard, error)
type RollbackCoordinator interface {
	Preview(context.Context, string, string, domain.User) (RollbackPreview, error)
	Enqueue(context.Context, string, string, string, bool, domain.User) (jobs.Job, error)
	Run(context.Context, *jobs.Execution) error
	Reconcile(context.Context, string, string, json.RawMessage) error
}
type Config struct {
	Rollbacks       RollbackCoordinator
	DB              *sql.DB
	Paths           instance.Paths
	Instances       *instance.Service
	Jobs            *jobs.Service
	Lifecycle       *lifecycle.Service
	Backups         *backup.Service
	Content         *content.Service
	Packs           *modpack.Service
	BeginUpdateStop BeginStop
}
type Service struct {
	config     Config
	mu         sync.Mutex
	registered bool
}

func New(config Config) (*Service, error) {
	if config.DB == nil || config.Instances == nil || config.Jobs == nil || config.Lifecycle == nil || config.Backups == nil || config.Content == nil || config.Packs == nil || config.BeginUpdateStop == nil {
		return nil, domain.E(domain.CodeUnavailable, "complete update transaction capabilities are required")
	}
	if _, err := config.Paths.For("update-capability-check"); err != nil {
		return nil, err
	}
	return &Service{config: config}, nil
}

func (s *Service) SetRollbackCoordinator(coordinator RollbackCoordinator) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.registered {
		return domain.E(domain.CodeConflict, "rollback capabilities are frozen after job registration")
	}
	if coordinator == nil {
		return domain.E(domain.CodeUnavailable, "rollback coordinator unavailable")
	}
	s.config.Rollbacks = coordinator
	return nil
}
func (s *Service) PreviewRollback(ctx context.Context, id, transaction string, actor domain.User) (RollbackPreview, error) {
	if s.config.Rollbacks == nil {
		return RollbackPreview{}, domain.E(domain.CodeUnavailable, "explicit instance rollback unavailable")
	}
	return s.config.Rollbacks.Preview(ctx, id, transaction, actor)
}
func (s *Service) EnqueueRollback(ctx context.Context, id, preview, confirmation string, ack bool, actor domain.User) (jobs.Job, error) {
	if s.config.Rollbacks == nil {
		return jobs.Job{}, domain.E(domain.CodeUnavailable, "explicit instance rollback unavailable")
	}
	return s.config.Rollbacks.Enqueue(ctx, id, preview, confirmation, ack, actor)
}
