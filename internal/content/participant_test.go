package content

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestPackParticipantRejectsForgedAndExpiredCapabilities(t *testing.T) {
	p := &PackParticipant{}
	if e := p.Commit(t.Context(), nil); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("forged capability", e)
	}
	if e := p.ValidatePostimages(t.Context()); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal("forged validation", e)
	}
}
