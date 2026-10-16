package world

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type fixtureStore struct{ detail instance.Detail }

func (s fixtureStore) Get(context.Context, string) (instance.Detail, error) { return s.detail, nil }

type reservation struct{}

func (reservation) WithIdle(_ context.Context, _ string, fn func() error) error { return fn() }
func fixture(t *testing.T, version string, flavor domain.Flavor) (*Service, string) {
	t.Helper()
	data := t.TempDir()
	p, e := instance.NewPaths(data)
	if e != nil {
		t.Fatal(e)
	}
	dirs, e := p.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range []string{dirs.Server, dirs.State} {
		if e = os.MkdirAll(d, 0700); e != nil {
			t.Fatal(e)
		}
	}
	s := New(nil, fixtureStore{instance.Detail{Instance: domain.Instance{ID: "alpha", MinecraftVersion: version, Flavor: flavor}, Config: domain.InstanceConfig{LevelName: "My world"}}}, p, reservation{})
	return s, dirs.Server
}
func put(t *testing.T, base, name, value string) {
	t.Helper()
	p := filepath.Join(base, filepath.FromSlash(name))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestDiscoverActualVersionedLayouts(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		flavor        domain.Flavor
		paths         []string
		layout        string
		dimensions    int
	}{
		{"legacy", "1.12.2", domain.FlavorVanilla, []string{"My world/level.dat", "My world/region/r.0.0.mca", "My world/DIM-1/region/r.0.0.mca", "My world/DIM1/region/r.0.0.mca"}, "legacy", 3},
		{"custom", "1.20.5", domain.FlavorFabric, []string{"My world/level.dat", "My world/region/r.0.0.mca", "My world/dimensions/example/moon/region/r.0.0.mca", "My world/dimensions/example/deep/moon/region/r.0.0.mca"}, "legacy", 5},
		{"paper", "1.21.9", domain.FlavorPaper, []string{"My world/level.dat", "My world/region/r.0.0.mca", "My world_nether/DIM-1/region/r.0.0.mca", "My world_the_end/DIM1/region/r.0.0.mca"}, "paper_legacy", 3},
		{"modern", "26.1", domain.FlavorVanilla, []string{"My world/level.dat", "My world/dimensions/minecraft/overworld/region/r.0.0.mca", "My world/dimensions/minecraft/the_nether/region/r.0.0.mca", "My world/players/data/player.dat"}, "namespaced_26_1", 3},
		{"paper modern", "26.1", domain.FlavorPaper, []string{"My world/level.dat", "My world/dimensions/minecraft/overworld/region/r.0.0.mca"}, "namespaced_26_1", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, base := fixture(t, tc.version, tc.flavor)
			for _, p := range tc.paths {
				put(t, base, p, "save")
			}
			d, e := s.Discover(context.Background(), "alpha")
			if e != nil {
				t.Fatal(e)
			}
			if d.State != "present" || d.Layout != tc.layout || len(d.Dimensions) != tc.dimensions {
				t.Fatalf("unexpected discovery: %+v", d)
			}
			for _, dim := range d.Dimensions {
				if filepath.IsAbs(dim.Path) {
					t.Fatal("absolute path exposed")
				}
			}
		})
	}
}
func TestDiscoverAbsentAndAmbiguous(t *testing.T) {
	s, base := fixture(t, "26.1", domain.FlavorPaper)
	d, e := s.Discover(context.Background(), "alpha")
	if e != nil || d.State != "absent" {
		t.Fatal(d, e)
	}
	put(t, base, "My world/level.dat", "save")
	put(t, base, "My world_nether/DIM-1/region/r.0.0.mca", "old")
	d, e = s.Discover(context.Background(), "alpha")
	if e != nil || d.State != "ambiguous" {
		t.Fatal(d, e)
	}
}
func TestDiscoveryRefusesTraversalUnsafeNamesAndLinks(t *testing.T) {
	for _, name := range []string{"../secret", "a\\b", "CON", "world.", "world\nname"} {
		t.Run(name, func(t *testing.T) {
			s, _ := fixture(t, "1.21.9", domain.FlavorVanilla)
			s.instances = fixtureStore{instance.Detail{Instance: domain.Instance{MinecraftVersion: "1.21.9", Flavor: domain.FlavorVanilla}, Config: domain.InstanceConfig{LevelName: name}}}
			if _, e := s.Discover(context.Background(), "alpha"); e == nil {
				t.Fatal("accepted unsafe world name")
			}
		})
	}
	s, base := fixture(t, "1.21.9", domain.FlavorVanilla)
	put(t, base, "My world/level.dat", "save")
	if e := os.Symlink(t.TempDir(), filepath.Join(base, "My world", "dimensions")); e != nil {
		t.Skip(e)
	}
	if _, e := s.Discover(context.Background(), "alpha"); e == nil {
		t.Fatal("accepted linked dimensions")
	}
}
