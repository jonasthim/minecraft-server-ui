package update

import (
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
)

// These regressions catch caller-controlled automatic targets, stale confirmation,
// invalid artifact identity, and mutation of a frozen transaction plan.
func TestAutomaticRequestRejectsEveryCallerTarget(t *testing.T) {
	current := domain.VersionSelection{Flavor: domain.FlavorPaper, MinecraftVersion: "1.21.1", LoaderVersion: "100"}
	for _, request := range []Request{
		{Mode: Automatic, Selection: &current},
		{Mode: Automatic, ContentSelections: map[string]ContentSelection{"mod": {Provider: "modrinth", ProjectID: "p", VersionID: "v"}}},
		{Mode: Automatic, Pack: &modpack.Request{}},
	} {
		if _, _, err := request.Resolve(current); err == nil {
			t.Fatal("automatic request accepted caller target")
		}
	}
	chosen, countdown, err := (Request{Mode: Automatic}).Resolve(current)
	if err != nil || chosen != current || countdown != 300 {
		t.Fatalf("automatic pin/default: %v %d %v", chosen, countdown, err)
	}
}

func TestManualRequestRequiresExactSelectionAndBoundedCountdown(t *testing.T) {
	selection := domain.VersionSelection{Flavor: domain.FlavorFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.16.5"}
	zero := 0
	if _, _, err := (Request{Mode: Manual}).Resolve(selection); err == nil {
		t.Fatal("missing manual selection accepted")
	}
	chosen, countdown, err := (Request{Mode: Manual, Selection: &selection, CountdownSeconds: &zero}).Resolve(selection)
	if err != nil || chosen != selection || countdown != 0 {
		t.Fatalf("explicit zero lost: %v %d %v", chosen, countdown, err)
	}
	bad := 3601
	if _, _, err = (Request{Mode: Manual, Selection: &selection, CountdownSeconds: &bad}).Resolve(selection); err == nil {
		t.Fatal("unbounded countdown accepted")
	}
	selection.MinecraftVersion = "latest"
	if _, _, err = (Request{Mode: Manual, Selection: &selection}).Resolve(selection); err == nil {
		t.Fatal("floating version accepted")
	}
}

func planFixture() Plan {
	return Plan{
		ID: strings.Repeat("a", 32), InstanceID: "survival", ActorID: 1, Mode: Manual,
		Snapshot:     Snapshot{InstanceRevision: "revision-1", Generation: strings.Repeat("b", 32), ContentRevision: strings.Repeat("c", 64), PackRevision: "none"},
		From:         domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.1"},
		Target:       domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.2"},
		Requirements: domain.LaunchRequirements{JavaMajor: 21, MinJavaMajor: 21, MaxJavaMajor: 21},
		Runtime:      RuntimeIdentity{ID: "temurin-21", Major: 21, Version: "21.0.4+7", OS: "linux", Architecture: "x64", SHA256: strings.Repeat("d", 64)},
		Artifacts:    []Artifact{{Filename: "server.jar", Size: 100, SHA256: strings.Repeat("e", 64)}},
		Changes:      []Change{{Component: "server", Before: "1.21.1", After: "1.21.2"}},
		Blockers:     []Blocker{}, CountdownSeconds: 300, ExpiresAt: time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC),
	}
}

func TestFrozenPlanRejectsStaleConfirmationAndUnavailableSafetyGates(t *testing.T) {
	p := planFixture()
	frozen, err := Freeze(p)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err = frozen.CheckApply(p.Snapshot, 1, now, true); err != nil {
		t.Fatal(err)
	}
	stale := p.Snapshot
	stale.Generation = strings.Repeat("f", 32)
	if err = frozen.CheckApply(stale, 1, now, true); err == nil {
		t.Fatal("stale generation accepted")
	}
	if err = frozen.CheckApply(p.Snapshot, 2, now, true); err == nil {
		t.Fatal("foreign actor accepted")
	}
	if err = frozen.CheckApply(p.Snapshot, 1, p.ExpiresAt, true); err == nil {
		t.Fatal("expired confirmation accepted")
	}
	if err = frozen.CheckApply(p.Snapshot, 1, now, false); err == nil {
		t.Fatal("unconfirmed manual change accepted")
	}
	p.Blockers = []Blocker{{Code: "unknown_content", Message: "Review unknown upload"}}
	frozen, err = Freeze(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = frozen.CheckApply(p.Snapshot, 1, now, true); err == nil {
		t.Fatal("blocked preview accepted")
	}
}

func TestFrozenPlanCopiesInputsAndOutputsAndBindsExactAutomaticPin(t *testing.T) {
	p := planFixture()
	frozen, err := Freeze(p)
	if err != nil {
		t.Fatal(err)
	}
	digest := frozen.Digest()
	p.Artifacts[0].SHA256 = strings.Repeat("f", 64)
	view := frozen.View()
	if view.Artifacts[0].SHA256 != strings.Repeat("e", 64) {
		t.Fatal("caller changed frozen artifacts")
	}
	view.Artifacts[0].SHA256 = strings.Repeat("a", 64)
	if frozen.View().Artifacts[0].SHA256 != strings.Repeat("e", 64) || frozen.Digest() != digest {
		t.Fatal("view changed frozen plan")
	}
	p = planFixture()
	p.Mode = Automatic
	if _, err = Freeze(p); err == nil {
		t.Fatal("automatic Minecraft version change accepted")
	}
	p.Target = p.From
	if _, err = Freeze(p); err != nil {
		t.Fatal(err)
	}
	p.Target.Flavor = domain.FlavorPaper
	p.Target.LoaderVersion = "123"
	if _, err = Freeze(p); err == nil {
		t.Fatal("automatic flavor change accepted")
	}
}

func TestFrozenPlanRejectsUnverifiedArtifactAndRuntimeIdentity(t *testing.T) {
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Artifacts[0].SHA256 = "" },
		func(p *Plan) { p.Artifacts[0].Filename = "../server.jar" },
		func(p *Plan) { p.Runtime.SHA256 = "" },
		func(p *Plan) { p.Runtime.Major = 17 },
		func(p *Plan) { p.Snapshot.ContentRevision = "" },
	} {
		p := planFixture()
		mutate(&p)
		if _, err := Freeze(p); err == nil {
			t.Fatal("incomplete or unsafe plan accepted")
		}
	}
}

func TestPlanDigestExcludesOnlyPackObservationIdentity(t *testing.T) {
	p := planFixture()
	p.Pack = &modpack.Preview{ID: "ephemeral-one", Source: modpack.Source{Name: "same"}, Selections: map[string]modpack.Selection{"mods/a.jar": {Selected: true}}}
	first, err := Freeze(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Pack.ID = "ephemeral-two"
	p.Pack.Source.Provenance.FetchedAt = time.Now()
	same, err := Freeze(p)
	if err != nil || first.Digest() != same.Digest() {
		t.Fatal("ephemeral observation changed confirmation", err)
	}
	p.Pack.Selections["mods/a.jar"] = modpack.Selection{Selected: true, Pinned: true}
	different, err := Freeze(p)
	if err != nil || different.Digest() == same.Digest() {
		t.Fatal("meaningful selection omitted from confirmation", err)
	}
}
