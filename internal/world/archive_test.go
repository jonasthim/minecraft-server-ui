package world

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

func exportFixture(t *testing.T) (*Service, string) {
	t.Helper()
	s, base := fixture(t, "1.21.9", domain.FlavorPaper)
	database, e := db.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	s.db = database
	for name, value := range map[string]string{"My world/level.dat": "level", "My world/region/r.0.0.mca": "overworld", "My world/playerdata/player.dat": "player", "My world/dimensions/example/moon/region/r.0.0.mca": "moon", "My world_nether/DIM-1/region/r.0.0.mca": "nether", "My world_the_end/DIM1/region/r.0.0.mca": "end", "server.properties": "server secret", "My world/server.properties": "nested secret", "My world/.env": "credential", "My world/.custom-mod-save": "mod hidden", "My world/custom.yml": "mod yaml", "My world/custom.properties": "mod properties", "My world/session.lock": "lock"} {
		put(t, base, name, value)
	}
	return s, base
}

var operator = domain.User{ID: 7, Username: "operator", Role: domain.RoleOperator}

type captureFailureStore struct {
	get func(context.Context, string) (instance.Detail, error)
}

func (s captureFailureStore) Get(ctx context.Context, id string) (instance.Detail, error) {
	return s.get(ctx, id)
}

// A source refresh failure after staging must not hide a simultaneous failure
// to remove the staged export. Inject both through real filesystem operations.
func TestCaptureReportsOriginalAndCleanupFailures(t *testing.T) {
	s, base := exportFixture(t)
	store := s.instances
	captureFailure := errors.New("injected source refresh failure")
	var retained, stagedName string
	calls := 0
	s.instances = captureFailureStore{get: func(ctx context.Context, id string) (instance.Detail, error) {
		calls++
		if calls == 1 {
			return store.Get(ctx, id)
		}
		dir := filepath.Join(filepath.Dir(base), "backups", ".exports")
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("staged archive absent: %v %v", entries, err)
		}
		stagedName = entries[0].Name()
		staged := filepath.Join(dir, stagedName)
		moved := staged + ".retained"
		if err := os.Rename(staged, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(staged, 0700); err != nil {
			t.Fatal(err)
		}
		retained = filepath.Join(staged, "retained.zip")
		if err := os.Rename(moved, retained); err != nil {
			t.Fatal(err)
		}
		return instance.Detail{}, captureFailure
	}}
	a, err := s.Capture(context.Background(), operator, "alpha")
	if a != nil {
		a.Close()
		t.Fatal("failed capture returned archive")
	}
	if !errors.Is(err, captureFailure) {
		t.Fatal("original capture failure lost", err)
	}
	var cleanupFailure *os.PathError
	if !errors.As(err, &cleanupFailure) || filepath.Base(cleanupFailure.Path) != stagedName {
		t.Fatal("staged removal failure lost", err)
	}
	if info, statErr := os.Stat(retained); statErr != nil || info.Size() == 0 {
		t.Fatal("fixture did not retain staged export", info, statErr)
	}
}

func TestCapturePreservesAssociatedRootsAndValidatesDigest(t *testing.T) {
	s, base := exportFixture(t)
	a, e := s.Capture(context.Background(), operator, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	zr, e := zip.NewReader(a, a.Size)
	if e != nil {
		t.Fatal(e)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		got[f.Name] = string(b)
	}
	for name, want := range map[string]string{"My world/level.dat": "level", "My world/region/r.0.0.mca": "overworld", "My world/playerdata/player.dat": "player", "My world/dimensions/example/moon/region/r.0.0.mca": "moon", "My world_nether/DIM-1/region/r.0.0.mca": "nether", "My world_the_end/DIM1/region/r.0.0.mca": "end"} {
		if got[name] != want {
			t.Fatal(name, got)
		}
	}
	for name := range got {
		if strings.HasSuffix(name, "/server.properties") || strings.Contains(name, ".env") || strings.Contains(name, "session.lock") {
			t.Fatal("secret exported", name)
		}
	}
	for name, want := range map[string]string{"My world/.custom-mod-save": "mod hidden", "My world/custom.yml": "mod yaml", "My world/custom.properties": "mod properties"} {
		if got[name] != want {
			t.Fatal("mod data omitted", name, got)
		}
	}
	if len(got) != 10 {
		t.Fatal(got)
	}
	a.Seek(0, io.SeekStart)
	h := sha256.New()
	if _, e = io.Copy(h, a); e != nil {
		t.Fatal(e)
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		t.Fatal("digest mismatch")
	}
	var n int
	if e = s.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='world.export_prepared' AND user_id=7`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	files, e := os.ReadDir(filepath.Join(filepath.Dir(base), "backups", ".exports"))
	if e != nil || len(files) != 0 {
		t.Fatal(files, e)
	}
}
func TestCaptureRejectsBoundsCancelAndUnprivilegedWithoutArchive(t *testing.T) {
	for _, tc := range []string{"size", "entries", "depth", "file-size", "archive-size", "path-size", "dimensions", "cancel", "viewer", "audit", "ambiguous", "absent", "symlink"} {
		t.Run(tc, func(t *testing.T) {
			s, base := exportFixture(t)
			ctx := context.Background()
			actor := operator
			switch tc {
			case "size":
				s.limits.bytes = 2
			case "file-size":
				s.limits.fileBytes = 2
			case "archive-size":
				s.limits.archiveBytes = 8
			case "path-size":
				s.limits.pathBytes = 2
			case "dimensions":
				s.limits.dimensions = 2
			case "entries":
				s.limits.entries = 2
			case "depth":
				s.limits.depth = 1
			case "cancel":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "viewer":
				actor.Role = domain.RoleViewer
			case "audit":
				s.db.Close()
			case "ambiguous":
				put(t, base, "My world/DIM-1/region/r.0.0.mca", "old")
			case "absent":
				os.RemoveAll(filepath.Join(base, "My world"))
			case "symlink":
				if e := os.Symlink(filepath.Join(base, "server.properties"), filepath.Join(base, "My world", "secret.dat")); e != nil {
					t.Skip(e)
				}
			}
			a, e := s.Capture(ctx, actor, "alpha")
			if a != nil {
				a.Close()
			}
			if e == nil {
				t.Fatal("invalid capture succeeded")
			}
			files, e := os.ReadDir(filepath.Join(filepath.Dir(base), "backups", ".exports"))
			if e != nil && !os.IsNotExist(e) {
				t.Fatal(e)
			}
			if len(files) != 0 {
				t.Fatal("failed capture leaked staging", files)
			}
		})
	}
}
func TestCaptureGlobalAdmissionEndsOnClose(t *testing.T) {
	s, _ := exportFixture(t)
	a, e := s.Capture(context.Background(), operator, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := s.Capture(context.Background(), operator, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if c, e := s.Capture(context.Background(), operator, "alpha"); e == nil {
		c.Close()
		t.Fatal("third export admitted")
	}
	a.Close()
	c, e := s.Capture(context.Background(), operator, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
}

type changingWriter struct {
	path     string
	changed  bool
	preserve bool
}

func (w *changingWriter) Write(b []byte) (int, error) {
	if !w.changed {
		info, e := os.Stat(w.path)
		if e != nil {
			return 0, e
		}
		w.changed = true
		if e = os.WriteFile(w.path, []byte("rewritten"), 0600); e != nil {
			return 0, e
		}
		if w.preserve {
			if e = os.Chtimes(w.path, info.ModTime(), info.ModTime()); e != nil {
				return 0, e
			}
		}
	}
	return len(b), nil
}

// The writer changes the actual source during its copy; accepting the original
// size/mtime alone or removing the identity checks would make this test fail.
func TestSourceChangeDuringCopyFails(t *testing.T) {
	s, base := exportFixture(t)
	r, e := os.OpenRoot(filepath.Join(base, "My world"))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	entries, e := s.inventory(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.name == "region/r.0.0.mca" {
			_, e = readEntry(context.Background(), r, entry, &changingWriter{path: filepath.Join(base, "My world", filepath.FromSlash(entry.name))})
			if e == nil {
				t.Fatal("changed file accepted")
			}
			return
		}
	}
	t.Fatal("missing region fixture")
}
func TestSecondDigestDetectsSameSizeRestoredTimestamp(t *testing.T) {
	s, _ := exportFixture(t)
	d, roots, e := s.discover(context.Background(), "alpha", true)
	if e != nil || d.State != "present" {
		t.Fatal(d, e)
	}
	defer closeRoots(roots)
	digests := map[string]string{}
	for _, root := range roots {
		for _, entry := range root.entries {
			if entry.omitted || entry.info.IsDir() {
				continue
			}
			digest, e := readEntry(context.Background(), root.root, entry, io.Discard)
			if e != nil {
				t.Fatal(e)
			}
			digests[root.name+"/"+entry.name] = digest
		}
	}
	for _, root := range roots {
		if root.name != "My world" {
			continue
		}
		f, e := root.root.OpenFile("level.dat", os.O_WRONLY, 0)
		if e != nil {
			t.Fatal(e)
		}
		info, e := f.Stat()
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.WriteAt([]byte("other"), 0); e != nil {
			t.Fatal(e)
		}
		f.Close()
		if e = root.root.Chtimes("level.dat", info.ModTime(), info.ModTime()); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.validateRoots(context.Background(), "alpha", roots, digests); e == nil {
		t.Fatal("same-size restored-mtime change accepted")
	}
}
func TestReservedConfiguredBoundaryRefused(t *testing.T) {
	s, _ := fixture(t, "1.21.9", domain.FlavorVanilla)
	store := s.instances.(fixtureStore)
	store.detail.Config.LevelName = ".manager"
	s.instances = store
	if _, e := s.Discover(context.Background(), "alpha"); e == nil {
		t.Fatal("reserved world root accepted")
	}
}

func TestDiscoveryListsExactOmissionsAndPreservesModData(t *testing.T) {
	s, _ := exportFixture(t)
	d, e := s.Discover(context.Background(), "alpha")
	if e != nil {
		t.Fatal(e)
	}
	omitted := map[string]string{}
	for _, item := range d.Omitted {
		omitted[item.Path] = item.Reason
	}
	for _, p := range []string{"My world/session.lock", "My world/.env", "My world/server.properties"} {
		if omitted[p] == "" {
			t.Fatal("missing omission disclosure", p, d.Omitted)
		}
	}
	for _, p := range []string{"My world/.custom-mod-save", "My world/custom.yml", "My world/custom.properties"} {
		if omitted[p] != "" {
			t.Fatal("ordinary mod data omitted", p)
		}
	}
}

type linkReplacingWriter struct {
	source, target string
	once           bool
}

func (w *linkReplacingWriter) Write(p []byte) (int, error) {
	if !w.once {
		w.once = true
		if e := os.Remove(w.source); e != nil {
			return 0, e
		}
		if e := os.Symlink(w.target, w.source); e != nil {
			return 0, e
		}
	}
	return len(p), nil
}
func TestSymlinkReplacementDuringCopyIsRefused(t *testing.T) {
	s, base := exportFixture(t)
	r, e := os.OpenRoot(filepath.Join(base, "My world"))
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	entries, e := s.inventory(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range entries {
		if entry.name != "level.dat" {
			continue
		}
		_, e = readEntry(context.Background(), r, entry, &linkReplacingWriter{source: filepath.Join(base, "My world", "level.dat"), target: filepath.Join(base, "server.properties")})
		if e == nil {
			t.Fatal("symlink replacement accepted")
		}
		return
	}
	t.Fatal("missing fixture")
}
func TestCredentialCandidatesAndPortableCollisionsRefused(t *testing.T) {
	for _, name := range []string{"private.key", "private.pem", "CON.txt", "trailing.", "control\x7f", "Case.dat"} {
		t.Run(name, func(t *testing.T) {
			s, base := exportFixture(t)
			put(t, base, "My world/"+name, "value")
			if name == "Case.dat" {
				put(t, base, "My world/case.dat", "collision")
			}
			a, e := s.Capture(context.Background(), operator, "alpha")
			if a != nil {
				a.Close()
			}
			if e == nil {
				t.Fatal("unsafe world capture accepted")
			}
		})
	}
}
func TestCaptureModernPlayersAndDimensions(t *testing.T) {
	s, base := fixture(t, "26.1", domain.FlavorPaper)
	database, e := db.OpenMemory(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	s.db = database
	for _, p := range []string{"My world/level.dat", "My world/dimensions/minecraft/overworld/region/r.0.0.mca", "My world/dimensions/minecraft/the_nether/region/r.0.0.mca", "My world/dimensions/minecraft/the_end/region/r.0.0.mca", "My world/players/data/uuid.dat", "My world/players/stats/uuid.json", "My world/players/advancements/uuid.json"} {
		put(t, base, p, p)
	}
	a, e := s.Capture(context.Background(), operator, "alpha")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	z, e := zip.NewReader(a, a.Size)
	if e != nil {
		t.Fatal(e)
	}
	files := map[string]bool{}
	for _, f := range z.File {
		files[f.Name] = true
	}
	for _, p := range []string{"My world/players/data/uuid.dat", "My world/players/stats/uuid.json", "My world/players/advancements/uuid.json", "My world/dimensions/minecraft/the_nether/region/r.0.0.mca"} {
		if !files[p] {
			t.Fatal("modern world file absent", p)
		}
	}
}
