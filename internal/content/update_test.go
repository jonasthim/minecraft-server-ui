package content

import (
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
)

func TestTargetUpdateUsesNewTupleAndPreservesUnknownAndPinnedGates(t *testing.T) {
	proof := lifecycle.TargetProof{Selection: domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.2", LoaderVersion: "0.15.0"}, Requirements: domain.LaunchRequirements{JavaMajor: 17}, Runtime: domain.JavaRuntime{ID: "target17", Major: 17}}
	base := Installed{ID: "retained", Kind: catalog.Mod, Name: "Retained", Version: "1.0", Enabled: true, Pinned: true, SHA256: strings.Repeat("a", 64), Files: []OwnedFile{{Path: "mods/retained.jar", SHA256: strings.Repeat("a", 64)}}, Descriptor: Descriptor{ID: "retained", Version: "1.0", Loader: "fabric", Minecraft: "=1.20.2", Required: map[string]string{}, Conflicts: map[string]string{}}}
	items, blockers, err := PrepareTargetCandidate(proof, []Installed{base})
	if err != nil || len(blockers) > 0 || len(items) != 1 {
		t.Fatal(items, blockers, err)
	}
	old := proof
	old.Selection.MinecraftVersion = "1.20.1"
	if _, blockers, err = PrepareTargetCandidate(old, []Installed{base}); err != nil || len(blockers) == 0 {
		t.Fatal("current tuple blessed target-only content", blockers, err)
	}
	base.Descriptor.Minecraft = "=1.20.1"
	if _, blockers, err = PrepareTargetCandidate(proof, []Installed{base}); err != nil || len(blockers) == 0 {
		t.Fatal("pinned incompatible item accepted", blockers, err)
	}
	base.Descriptor.Minecraft = "=1.20.2"
	base.Unknown = true
	base.TrialTuple = &Tuple{Minecraft: "1.20.2", Loader: "fabric", LoaderVersion: "0.15.0"}
	if items, blockers, err = PrepareTargetCandidate(proof, []Installed{base}); err != nil || len(blockers) == 0 || !items[0].Unknown {
		t.Fatal("unknown trial relabelled known", items, blockers, err)
	}
}
func TestTargetUpdateCannotReuseCurrentForgeLanguageEvidence(t *testing.T) {
	selection := domain.VersionSelection{Flavor: domain.FlavorForge, MinecraftVersion: "1.20.1", LoaderVersion: "47.3.0"}
	stale := loaderutil.LanguageEvidence{Selection: selection, RuntimeID: "old", JavaMajor: 17, Languages: map[string]loaderutil.LanguageArtifact{}}
	item := Installed{ID: "example", Kind: catalog.Mod, Enabled: true, LanguageEvidence: &stale, Descriptor: Descriptor{ID: "example", Version: "1.0", Loader: "forge", Minecraft: "[1.20.1,1.21)", LanguageLoader: "javafml", LanguageRequirement: "[47,)", Required: map[string]string{}, Conflicts: map[string]string{}}}
	proof := lifecycle.TargetProof{Selection: selection, Runtime: domain.JavaRuntime{ID: "target", Major: 17}}
	_, blockers, err := PrepareTargetCandidate(proof, []Installed{item})
	if err != nil || len(blockers) == 0 {
		t.Fatal("stale current language evidence qualified target", blockers, err)
	}
}
