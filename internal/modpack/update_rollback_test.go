package modpack

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestRollbackPackRejectsForgedCapability(t *testing.T) {
	p := &RollbackParticipant{}
	if err := p.Commit(t.Context(), nil); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
}
