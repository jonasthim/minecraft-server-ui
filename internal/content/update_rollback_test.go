package content

import (
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"reflect"
	"testing"
)

func TestRollbackContentRejectsForgedCapability(t *testing.T) {
	p := &RollbackParticipant{}
	if err := p.ValidatePostimages(t.Context()); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
	if err := p.Commit(t.Context(), nil); domain.AsError(err).Code != domain.CodeForbidden {
		t.Fatal(err)
	}
}

func TestRollbackCandidateRequiresFreshHistoricalLanguageProof(t *testing.T) {
	tuple := Tuple{Minecraft: "1.20.1", Loader: "forge", LoaderVersion: "47.4.0"}
	selection := domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: tuple.Minecraft, LoaderVersion: tuple.LoaderVersion}
	stale := loaderutil.LanguageEvidence{Selection: selection, Generation: "old cached generation", Languages: map[string]loaderutil.LanguageArtifact{"javafml": {Version: "47.4.0"}}}
	fresh := loaderutil.LanguageEvidence{Selection: selection, Generation: "fresh verified historical generation", RuntimeID: "historical-java", JavaMajor: 17, Languages: map[string]loaderutil.LanguageArtifact{"javafml": {Version: "47.4.0"}}}
	manifest := Manifest{Tuple: tuple, Items: []Installed{{ID: "known", Kind: catalog.Mod, Enabled: true, Descriptor: Descriptor{ID: "example", Version: "1.0", Loader: "forge", Minecraft: "1.20.1", LanguageLoader: "javafml", LanguageRequirement: "[47,)"}, LanguageEvidence: &stale}, {ID: "unknown-disabled", Unknown: true, Enabled: false}}}
	proof := lifecycle.TargetProof{Selection: selection, Runtime: domain.JavaRuntime{ID: "historical-java", Major: 17}, Language: fresh}
	candidate, err := rollbackCandidate(proof, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidate[0].LanguageEvidence, &fresh) || !candidate[1].Unknown {
		t.Fatalf("stale proof or lost provenance: %+v", candidate)
	}
	if manifest.Items[0].LanguageEvidence.Generation != "old cached generation" {
		t.Fatal("historical manifest mutated")
	}
	proof.Language = loaderutil.LanguageEvidence{}
	if _, err = rollbackCandidate(proof, manifest); err == nil {
		t.Fatal("stale historical proof survived failed fresh verification")
	}
}
