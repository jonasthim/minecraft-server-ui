package instance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type testGuard struct {
	mu      sync.Mutex
	blocked bool
}

func (g *testGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.blocked {
		return domain.E(domain.CodeInstanceRunning, "running")
	}
	return fn()
}
func setup(t *testing.T) (*Service, Paths, *testGuard) {
	t.Helper()
	database, e := db.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	p, e := NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	g := &testGuard{}
	return New(database, p, g), p, g
}

var admin = domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}

func draft(id string, port int) Draft {
	return Draft{Instance: domain.Instance{ID: id, Name: id, Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9", JavaMajor: 21, MemoryMiB: 1024, GamePort: port}, Config: DefaultConfig()}
}
func TestEmptyDBCRUDIsolationAndEULA(t *testing.T) {
	s, p, _ := setup(t)
	ctx := context.Background()
	a, e := s.Create(ctx, admin, draft("alpha", 25565))
	if e != nil {
		t.Fatal(e)
	}
	if a.EULA.Accepted {
		t.Fatal("implicit EULA")
	}
	d := draft("bravo", 25566)
	d.AcceptEULA = true
	b, e := s.Create(ctx, admin, d)
	if e != nil {
		t.Fatal(e)
	}
	if !b.EULA.Accepted || b.EULA.AcceptedBy != 1 || b.EULA.AcceptedAt == nil {
		t.Fatal(b.EULA)
	}
	all, e := s.List(ctx)
	if e != nil || len(all) != 2 {
		t.Fatal(all, e)
	}
	a.Instance.Name = "Renamed"
	a.Config.MOTD = "Hello\nworld=second"
	a.Config.Extra = map[string]string{"some.mod.option": "keep"}
	a, e = s.Update(ctx, admin, a.Instance.ID, Draft{Instance: a.Instance, Config: a.Config})
	if e != nil {
		t.Fatal(e)
	}
	dirs, _ := p.For("alpha")
	raw, e := os.ReadFile(filepath.Join(dirs.Server, "server.properties"))
	if e != nil || !strings.Contains(string(raw), `motd=Hello\nworld\=second`) {
		t.Fatal(string(raw), e)
	}
	if e = s.Delete(ctx, admin, "alpha", "alpha"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, "alpha"); domain.AsError(e).Code != domain.CodeNotFound {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, "bravo"); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(dirs.Root); !os.IsNotExist(e) {
		t.Fatal("directory retained", e)
	}
}
func TestValidationAndGuards(t *testing.T) {
	s, _, g := setup(t)
	ctx := context.Background()
	if _, e := s.Create(ctx, domain.User{Role: domain.RoleOperator}, draft("one", 25565)); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
	a, e := s.Create(ctx, admin, draft("one", 25565))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Create(ctx, admin, draft("two", 25565)); domain.AsError(e).Code != domain.CodePortInUse {
		t.Fatal(e)
	}
	g.blocked = true
	if _, e = s.Update(ctx, admin, "one", Draft{Instance: a.Instance, Config: a.Config}); domain.AsError(e).Code != domain.CodeInstanceRunning {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, admin, "one", "one"); domain.AsError(e).Code != domain.CodeInstanceRunning {
		t.Fatal(e)
	}
	g.blocked = false
	a.Config.Extra = map[string]string{"rcon.password": "secret"}
	if _, e = s.Update(ctx, admin, "one", Draft{Instance: a.Instance, Config: a.Config}); domain.AsError(e).Code != domain.CodeValidationFailed {
		t.Fatal(e)
	}
	a.Config = DefaultConfig()
	a.Config.ViewDistance = 33
	if _, e = s.Update(ctx, admin, "one", Draft{Instance: a.Instance, Config: a.Config}); e == nil {
		t.Fatal("bad bounds")
	}
}
func TestConcurrentPortReservation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for _, id := range []string{"alpha", "bravo"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, e := s.Create(ctx, admin, draft(id, 25565))
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
			} else if domain.AsError(e).Code != domain.CodePortInUse {
				t.Error(e)
			}
		}(id)
	}
	wg.Wait()
	if success != 1 {
		t.Fatal(success)
	}
}
func TestPortablePathsAndSymlinks(t *testing.T) {
	p, e := NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"../escape", "CON", "con", "aux", "com1", "lpt9", "a/b", "a\\b", "a.", "C:drive"} {
		if _, e = p.For(id); e == nil {
			t.Errorf("accepted %q", id)
		}
	}
	dirs, _ := p.For("alpha")
	if e = os.MkdirAll(dirs.Root, 0700); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e = os.Symlink(outside, dirs.Server); e != nil {
		t.Skip(e)
	}
	if _, e = p.For("alpha"); e == nil {
		t.Fatal("symlink accepted")
	}
	if _, e = p.Resolve("bravo", "server", "../alpha"); e == nil {
		t.Fatal("traversal")
	}
}

func TestUnknownFilePropertiesPreservedAndControlsRemoved(t *testing.T) {
	s, p, _ := setup(t)
	ctx := context.Background()
	a, e := s.Create(ctx, admin, draft("alpha", 25565))
	if e != nil {
		t.Fatal(e)
	}
	dirs, _ := p.For("alpha")
	f, e := os.OpenFile(filepath.Join(dirs.Server, "server.properties"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString("some.mod.flag = hello\\ world\\u263a\ncontinued=one\\\n  two\nenable-rcon=true\nrcon.password=never-copy\n")
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	a, e = s.Update(ctx, admin, "alpha", Draft{Instance: a.Instance, Config: a.Config})
	if e != nil {
		t.Fatal(e)
	}
	if a.Config.Extra["some.mod.flag"] != "hello world☺" || a.Config.Extra["continued"] != "onetwo" {
		t.Fatal(a.Config.Extra)
	}
	raw, e := os.ReadFile(filepath.Join(dirs.Server, "server.properties"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "never-copy") || strings.Contains(string(raw), "enable-rcon=true") {
		t.Fatal(string(raw))
	}
}
func TestNilGuardBlocksMutationsAndDeletePreservesCaches(t *testing.T) {
	s, p, g := setup(t)
	ctx := context.Background()
	a, e := s.Create(ctx, admin, draft("alpha", 25565))
	if e != nil {
		t.Fatal(e)
	}
	s.guard = nil
	if _, e = s.Update(ctx, admin, "alpha", Draft{Instance: a.Instance, Config: a.Config}); domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, admin, "alpha", "alpha"); domain.AsError(e).Code != domain.CodeUnavailable {
		t.Fatal(e)
	}
	s.guard = g
	dirs, _ := p.For("alpha")
	if e = os.MkdirAll(dirs.RuntimeCache, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dirs.RuntimeCache, "keep"), []byte("runtime"), 0600); e != nil {
		t.Fatal(e)
	}
	outside := t.TempDir()
	if e = os.WriteFile(filepath.Join(outside, "keep"), []byte("external"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(dirs.Server, "external-link")); e != nil {
		t.Skip(e)
	}
	if e = s.Delete(ctx, admin, "alpha", "alpha"); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{filepath.Join(dirs.RuntimeCache, "keep"), filepath.Join(outside, "keep")} {
		if _, e = os.Stat(path); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRuntimeDerivedAndStructuralFlavorFloors(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	d := draft("legacy", 25565)
	d.Instance.MinecraftVersion = "1.12.2"
	d.Instance.JavaMajor = 99
	a, e := s.Create(ctx, admin, d)
	if e != nil || a.Instance.JavaMajor != 8 {
		t.Fatal(a, e)
	}
	d = draft("fabric", 25566)
	d.Instance.Flavor = domain.FlavorFabric
	d.Instance.MinecraftVersion = "1.12.2"
	if _, e = s.Create(ctx, admin, d); domain.AsError(e).Code != domain.CodeValidationFailed {
		t.Fatal(e)
	}
}

func stageDeletion(t *testing.T, s *Service, p Paths, id string, committed bool) string {
	t.Helper()
	ctx := context.Background()
	token, e := s.ownerToken(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	staged := ".deleting-" + id + "-fault-boundary"
	if _, e = s.db.Exec(`INSERT INTO instance_mutations(instance_id,kind,staged_name,ownership_token) VALUES(?,'delete',?,?)`, id, staged, token); e != nil {
		t.Fatal(e)
	}
	dirs, e := p.For(id)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Rename(dirs.Root, filepath.Join(filepath.Dir(dirs.Root), staged)); e != nil {
		t.Fatal(e)
	}
	if committed {
		if _, e = s.db.Exec(`DELETE FROM instances WHERE id=?`, id); e != nil {
			t.Fatal(e)
		}
	}
	return filepath.Join(filepath.Dir(dirs.Root), staged)
}
func TestReconcileInterruptedDeleteBeforeAndAfterCommit(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "precommit", true: "committed"}[committed], func(t *testing.T) {
			s, p, _ := setup(t)
			ctx := context.Background()
			_, e := s.Create(ctx, admin, draft("alpha", 25565))
			if e != nil {
				t.Fatal(e)
			}
			dirs, _ := p.For("alpha")
			if e = os.WriteFile(filepath.Join(dirs.Server, "world-sentinel"), []byte("world"), 0600); e != nil {
				t.Fatal(e)
			}
			staged := stageDeletion(t, s, p, "alpha", committed)
			if _, e = s.Create(ctx, admin, draft("alpha", 25565)); domain.AsError(e).Code != domain.CodeConflict {
				t.Fatal("id reuse before replay", e)
			}
			result, e := s.Reconcile(ctx)
			if e != nil || len(result.Deferred) != 0 {
				t.Fatal(result, e)
			}
			if _, e = os.Stat(staged); !os.IsNotExist(e) {
				t.Fatal("staged retained", e)
			}
			if !committed {
				if raw, e := os.ReadFile(filepath.Join(dirs.Server, "world-sentinel")); e != nil || string(raw) != "world" {
					t.Fatal(string(raw), e)
				}
			} else {
				if _, e = os.Stat(dirs.Root); !os.IsNotExist(e) {
					t.Fatal("deleted root recreated", e)
				}
			}
			var n int
			if e = s.db.QueryRow(`SELECT COUNT(*) FROM instance_mutations`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
			if _, e = s.Reconcile(ctx); e != nil {
				t.Fatal("non-idempotent replay", e)
			}
		})
	}
}
func TestReconcileConfigDisagreementDefersLiveAndRestoresCanonical(t *testing.T) {
	s, p, g := setup(t)
	ctx := context.Background()
	_, e := s.Create(ctx, admin, draft("alpha", 25565))
	if e != nil {
		t.Fatal(e)
	}
	dirs, _ := p.For("alpha")
	path := filepath.Join(dirs.Server, "server.properties")
	if e = os.WriteFile(path, []byte("motd=uncommitted\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dirs.Server, "eula.txt"), []byte("eula=true\n"), 0600); e != nil {
		t.Fatal(e)
	}
	g.blocked = true
	result, e := s.Reconcile(ctx)
	if e != nil || len(result.Deferred) != 1 || result.Deferred[0].Code != domain.CodeInstanceRunning {
		t.Fatal(result, e)
	}
	if raw, _ := os.ReadFile(path); string(raw) != "motd=uncommitted\n" {
		t.Fatal("live config rewritten")
	}
	g.blocked = false
	if e = s.Materialize(ctx, "alpha"); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || !strings.Contains(string(raw), `motd=A\ Minecraft\ Server`) {
		t.Fatal(string(raw), e)
	}
	raw, e = os.ReadFile(filepath.Join(dirs.Server, "eula.txt"))
	if e != nil || string(raw) != "eula=false\n" {
		t.Fatal(string(raw), e)
	}
}
func TestReconcileNeverAdoptsUnknownDirectory(t *testing.T) {
	s, p, _ := setup(t)
	ctx := context.Background()
	_, e := s.Create(ctx, admin, draft("alpha", 25565))
	if e != nil {
		t.Fatal(e)
	}
	staged := stageDeletion(t, s, p, "alpha", false)
	dirs, _ := p.For("alpha")
	if e = os.MkdirAll(filepath.Join(dirs.Root, "state"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dirs.Root, "state", "instance-owner"), []byte("foreign"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reconcile(ctx); domain.AsError(e).Code != domain.CodeConflict {
		t.Fatal(e)
	}
	if _, e = os.Stat(staged); e != nil {
		t.Fatal("owned staged data lost", e)
	}
	raw, e := os.ReadFile(filepath.Join(dirs.Root, "state", "instance-owner"))
	if e != nil || string(raw) != "foreign" {
		t.Fatal("foreign data modified", e)
	}
}

func TestPaperBuildIsNumericPinnedSelection(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	d := draft("paper", 25565)
	d.Instance.Flavor = domain.FlavorPaper
	d.Instance.LoaderVersion = "42"
	a, e := s.Create(ctx, admin, d)
	if e != nil || a.Instance.LoaderVersion != "42" {
		t.Fatal(a, e)
	}
	d = draft("invalid-paper", 25566)
	d.Instance.Flavor = domain.FlavorPaper
	d.Instance.LoaderVersion = "latest"
	if _, e = s.Create(ctx, admin, d); domain.AsError(e).Code != domain.CodeValidationFailed {
		t.Fatal(e)
	}
}
