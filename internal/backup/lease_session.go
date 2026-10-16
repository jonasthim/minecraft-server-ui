package backup

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type backupSession struct{ *console.Session }

func (s backupSession) ReleaseSaving(ctx context.Context, id string) (domain.CommandResult, error) {
	return s.ReleaseSavingForBackup(ctx, id)
}
