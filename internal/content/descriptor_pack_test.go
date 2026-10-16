package content

import (
	"archive/zip"
	"bytes"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"testing"
)

func TestForgeClientDependencyDoesNotMakeContainingModClientOnly(t *testing.T) {
	for _, side := range []string{"CLIENT", "SERVER", "BOTH"} {
		raw := `modLoader="javafml"
loaderVersion="*"
license="MIT"
[[mods]]
modId="example"
version="1.0.0"
description='''Multiline
metadata'''
[[dependencies.example]]
modId="clientlib"
mandatory=true
versionRange="[1,)"
side="` + side + `"
`
		d, e := inspectJar(jarEntries(t, map[string]string{"META-INF/mods.toml": raw}))
		if e != nil || d.ClientOnly {
			t.Fatal(side, d, e)
		}
		_, present := d.Required["clientlib"]
		if present != (side != "CLIENT") {
			t.Fatalf("%s applicability %+v", side, d)
		}
	}
}
func TestNeoForgeDefaultsRequiredAndOptionalConstrainsInstalled(t *testing.T) {
	raw := `modLoader="javafml"
loaderVersion="*"
license="MIT"
[[mods]]
modId="example"
version="1"
[[dependencies.example]]
modId="requiredlib"
versionRange="[2,)"
[[dependencies.example]]
modId="optionallib"
type="optional"
versionRange="[3,)"
`
	d, e := inspectJar(jarEntries(t, map[string]string{"META-INF/neoforge.mods.toml": raw}))
	if e != nil {
		t.Fatal(e)
	}
	if d.Required["requiredlib"] != "[2,)" || d.Optional["optionallib"] != "[3,)" {
		t.Fatal(d)
	}
	d.Minecraft = "*"
	items := []Installed{{ID: "one", Enabled: true, Kind: catalog.Mod, Descriptor: d}, {ID: "two", Enabled: true, Kind: catalog.Mod, Descriptor: Descriptor{ID: "requiredlib", Version: "2", Loader: "neoforge", Minecraft: "*"}}, {ID: "three", Enabled: true, Kind: catalog.Mod, Descriptor: Descriptor{ID: "optionallib", Version: "2", Loader: "neoforge", Minecraft: "*"}}}
	if len(enabledManifestBlockers(Tuple{Minecraft: "1.21.1", Loader: "neoforge", LoaderVersion: "21.1.0"}, items)) == 0 {
		t.Fatal("optional installed incompatible version ignored")
	}
}
func TestFabricJavaRequirementIsNotDiscarded(t *testing.T) {
	d, e := inspectJar(jar(t, `{"id":"example","version":"1","depends":{"minecraft":"*","java":">=99"}}`))
	if e != nil {
		t.Fatal(e)
	}
	if d.JavaRequirement != ">=99" {
		t.Fatal("java constraint discarded", d)
	}
}
func TestNestedFabricIdentitiesAndConstraintsAreReadWithoutExecution(t *testing.T) {
	child := jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"nested","version":"2.0+build","depends":{"minecraft":["1.20.1","1.20.2"],"java":">=17","requiredlib":">=1"},"provides":["nested_alias"]}`})
	outer := jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"outer","version":"1","depends":{"minecraft":"1.20.1"},"jars":[{"file":"META-INF/jars/nested.jar"}]}`, "META-INF/jars/nested.jar": string(child)})
	d, e := inspectJar(outer)
	if e != nil || d.Unknown || len(d.Identities) != 2 || d.Identities[0].Required["requiredlib"] != ">=1" || d.JavaRequirement != ">=17" {
		t.Fatal(d, e)
	}
	if !satisfies("2.0+other", ">=2.0") {
		t.Fatal("Fabric build metadata incorrectly changes version ordering")
	}
}
func TestNestedFabricRefusesMissingUnsafeDuplicateAndDepth(t *testing.T) {
	for _, jars := range []string{`[{"file":"missing.jar"}]`, `[{"file":"../escape.jar"}]`, `[{"file":"nested.jar"},{"file":"nested.jar"}]`} {
		b := jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"outer","version":"1","jars":` + jars + `}`, "nested.jar": string(jar(t, fabric("nested", "1")))})
		if _, e := inspectJar(b); e == nil {
			t.Fatal("unsafe nested graph accepted", jars)
		}
	}
	b := jar(t, fabric("leaf", "1"))
	for i := 0; i < 6; i++ {
		b = jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"outer","version":"1","jars":[{"file":"nested.jar"}]}`, "nested.jar": string(b)})
	}
	if _, e := inspectJar(b); e == nil {
		t.Fatal("deep nested graph accepted")
	}
}
func TestHistoricalNeoModsTOMLUsesDeclaredNeoIdentityAndMandatoryFlags(t *testing.T) {
	raw := `modLoader="javafml"
loaderVersion="[2,)"
[[mods]]
modId="example"
version="1"
[[dependencies.example]]
modId="neoforge"
mandatory=true
versionRange="[20.4.253,)"
[[dependencies.example]]
modId="optional"
mandatory=false
versionRange="[3,)"
`
	d, e := inspectJar(jarEntries(t, map[string]string{"META-INF/mods.toml": raw}))
	if e != nil || d.Loader != "neoforge" || d.Optional["optional"] != "[3,)" || d.LanguageRequirement != "[2,)" {
		t.Fatal(d, e)
	}
}
func TestNestedFabricSharesAggregateBudgetAcrossSiblingArchives(t *testing.T) {
	child := jar(t, fabric("nested", "1"))
	outer := jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"outer","version":"1","jars":[{"file":"one.jar"},{"file":"two.jar"}]}`, "one.jar": string(child), "two.jar": string(child)})
	budget := &nestedBudget{bytes: (128 << 20) - int64(len(child))*2 + 1}
	if _, e := inspectArchiveTree(bytes.NewReader(outer), int64(len(outer)), 0, budget); e == nil {
		t.Fatal("nested sibling aggregate budget reset")
	}
}
func TestNestedFabricRejectsCompressedAmplification(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	header := &zip.FileHeader{Name: "padding", Method: zip.Store}
	f, _ := z.CreateHeader(header)
	f.Write(bytes.Repeat([]byte{0}, 2<<20))
	f, _ = z.Create("fabric.mod.json")
	f.Write([]byte(fabric("nested", "1")))
	z.Close()
	outer := jarEntries(t, map[string]string{"fabric.mod.json": `{"id":"outer","version":"1","jars":[{"file":"nested.jar"}]}`, "nested.jar": b.String()})
	if _, e := inspectJar(outer); e == nil {
		t.Fatal("amplified nested payload accepted")
	}
}
func TestWholeCandidateAllowsDependencyCyclesButRejectsOrderingCycles(t *testing.T) {
	tuple := Tuple{Minecraft: "1.20.1", Loader: "fabric", LoaderVersion: "0.16.0"}
	items := []Installed{}
	for i := 0; i < 160; i++ {
		id := fmt.Sprintf("mod%d", i)
		required := map[string]string{}
		if i == 0 {
			for j := 1; j < 160; j++ {
				required[fmt.Sprintf("mod%d", j)] = "*"
			}
		} else {
			required["mod0"] = "*"
		}
		items = append(items, Installed{ID: id, Kind: catalog.Mod, Enabled: true, Descriptor: Descriptor{ID: id, Version: "1", Loader: "fabric", Minecraft: "*", Required: required}})
	}
	if b := enabledManifestBlockers(tuple, items); len(b) > 0 {
		t.Fatal("large already-present dependency graph rejected", b)
	}
	items[0].Descriptor.Ordering = []DependencyOrdering{{Owner: "mod0", Target: "mod1", Direction: "BEFORE"}}
	items[1].Descriptor.Ordering = []DependencyOrdering{{Owner: "mod1", Target: "mod0", Direction: "BEFORE"}}
	if len(enabledManifestBlockers(tuple, items)) == 0 {
		t.Fatal("contradictory ordering cycle accepted")
	}
}
func TestJavaPatchRequirementCannotBeProvedFromRuntimeMajor(t *testing.T) {
	if javaMajorSatisfies(17, "<17.0.1") {
		t.Fatal("runtime major falsely proved patch upper bound")
	}
	if javaMajorSatisfies(17, ">=17.0.1") {
		t.Fatal("runtime major falsely proved patch lower bound")
	}
	if !javaMajorSatisfies(17, ">=17 <18") {
		t.Fatal("major constraint not recognized")
	}
}
