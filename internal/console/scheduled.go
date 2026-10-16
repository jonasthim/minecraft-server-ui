package console

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

// EnqueueCommandOnce preserves the approved command grammar and reauthorizes
// the account at admission; the existing worker checks it again at execution.
func (s *Service) EnqueueCommandOnce(ctx context.Context, key, id, text string, actor domain.User) (jobs.Job, error) {
	if key == "" {
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "scheduled command admission requires a durable key")
	}
	if actor.ID <= 0 || actor.Disabled {
		return jobs.Job{}, domain.E(domain.CodeForbidden, "active operator access required")
	}
	fresh := domain.User{ID: actor.ID}
	if err := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, actor.ID).Scan(&fresh.Username, &fresh.Role, &fresh.Disabled); err != nil {
		return jobs.Job{}, domain.E(domain.CodeForbidden, "command requester unavailable")
	}
	return s.enqueueCommand(ctx, key, id, text, fresh)
}
