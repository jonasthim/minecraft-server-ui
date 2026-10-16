//go:build windows

package launcher

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func inspectIdentity(ctx context.Context, id domain.ProcessIdentity) (string, string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	return processIdentity(id.PID)
}
