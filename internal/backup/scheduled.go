package backup

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

// EnqueueAutomaticOnce fixes the trigger to automatic; retention protection
// of public manual and safety backups remains a feature-owned decision.
func (s *Service) EnqueueAutomaticOnce(ctx context.Context, key, id string, kind domain.BackupKind, actor domain.User) (jobs.Job, error) {
	if key == "" {
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "scheduled backup admission requires a durable key")
	}
	return s.enqueue(ctx, key, id, kind, domain.BackupAutomatic, actor)
}
