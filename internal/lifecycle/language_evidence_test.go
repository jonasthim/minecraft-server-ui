package lifecycle

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
)

func TestLanguageEvidenceRefusesMissingPendingAndMismatchedInstallation(t *testing.T) {
	s, _, _ := fixture(t)
	tuple := domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.4.0"}
	if _, e := s.LanguageEvidence(t.Context(), "test", tuple); e == nil {
		t.Fatal("missing committed installation accepted")
	}
	if e := s.intent(t.Context(), "test", installation{Generation: "pending"}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.LanguageEvidence(t.Context(), "test", tuple); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("pending evidence admitted", e)
	}
	if _, e := s.db.Exec("DELETE FROM lifecycle_activations WHERE instance_id='test'"); e != nil {
		t.Fatal(e)
	}
	if e := s.saveInstallation(t.Context(), "test", installation{Generation: "committed", Plan: domain.InstallPlan{Selection: domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.0"}}}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.LanguageEvidence(t.Context(), "test", tuple); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal("mismatched tuple evidence admitted", e)
	}
}
