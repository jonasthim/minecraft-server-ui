package instance

import (
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestInstalledMemoryEditRequiresCoordinatorAndNoninstalledEditIsUnchanged(t *testing.T) {
	s, _, _ := setup(t)
	candidate := draft("alpha", 25565)
	if _, err := s.Create(t.Context(), admin, candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Instance.MemoryMiB = 2048
	if _, err := s.Update(t.Context(), admin, "alpha", candidate); err != nil {
		t.Fatalf("noninstalled memory blocked: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'`); err != nil {
		t.Fatal(err)
	}
	candidate.Instance.MemoryMiB = 4096
	if _, err := s.Update(t.Context(), admin, "alpha", candidate); domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatalf("missing coordinator admitted installed edit: %v", err)
	}
	detail, err := s.Get(t.Context(), "alpha")
	if err != nil || detail.Instance.MemoryMiB != 2048 {
		t.Fatalf("failed edit changed memory: %+v %v", detail, err)
	}
	candidate.Instance.MemoryMiB = 2048
	candidate.Instance.Name = "Same memory"
	if _, err = s.Update(t.Context(), admin, "alpha", candidate); err != nil {
		t.Fatalf("unrelated typed edit blocked: %v", err)
	}
}
