package lifecycle

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestRollbackLifecycleRejectsForgedCapability(t *testing.T) {
	p := &RollbackParticipant{}
	if err := p.Seal(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	if err := p.Commit(t.Context(), nil); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	if _, err := p.State(); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
}
