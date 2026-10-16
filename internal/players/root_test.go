package players

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

// Replay the production gap after Paths.For or StoppedSession.Paths returns and
// before player list root acquisition. Both reads and writes use this boundary.
func TestSavedListsRejectAncestorSwapAfterValidatedPaths(t *testing.T) {
	for _, operation := range []string{"read", "offline_write"} {
		t.Run(operation, func(t *testing.T) {
			paths, err := instance.NewPaths(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			dirs, err := paths.For("test")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(dirs.Server, 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			outsideServer := filepath.Join(outside, "server")
			if err = os.Mkdir(outsideServer, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(outsideServer, "whitelist.json")
			original := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"OutsideSecret"}]`
			if err = os.WriteFile(file, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			dirs, err = paths.For("test")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(dirs.Root, dirs.Root+".held"); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(outside, dirs.Root); err != nil {
				t.Skip(err)
			}
			pinned, err := openPlayerServer(dirs.Server)
			if err == nil {
				defer pinned.Close()
				if operation == "read" {
					list, e := savedList(context.Background(), pinned.root, "whitelist")
					if e == nil && len(list.Entries) > 0 {
						t.Errorf("ancestor substitution exposed outside saved identity: %+v", list.Entries)
					}
				} else {
					e := mutateSaved(context.Background(), pinned.root, console.PlayerMutation{Action: "whitelist_add", UUID: "00000000-0000-0000-0000-000000000002", Name: "Steve"}, time.Now())
					if e == nil {
						t.Error("ancestor substitution admitted offline mutation")
					}
				}
				t.Error("replaced ancestor admitted a player server root")
			}
			current, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if string(current) != original {
				t.Error("outside list changed")
			}
		})
	}
}

func TestPlayerServerRootPinsAndRechecksEveryAncestor(t *testing.T) {
	data := t.TempDir()
	server := filepath.Join(data, "instance", "server")
	if err := os.MkdirAll(server, 0700); err != nil {
		t.Fatal(err)
	}
	inside := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Inside"}]`
	if err := os.WriteFile(filepath.Join(server, "whitelist.json"), []byte(inside), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "server"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "server", "whitelist.json"), []byte(`[{"uuid":"00000000-0000-0000-0000-000000000002","name":"OutsideSecret"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	pinned, err := openPlayerServer(server)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	ancestor := filepath.Dir(server)
	if err = os.Rename(ancestor, ancestor+".held"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, ancestor); err != nil {
		t.Skip(err)
	}
	list, err := savedList(t.Context(), pinned.root, "whitelist")
	if err != nil || len(list.Entries) != 1 || list.Entries[0].Player.Name != "Inside" {
		t.Fatalf("pinned read escaped original tree: %+v %v", list, err)
	}
	if err = pinned.verify(); err == nil {
		t.Fatal("changed ancestor accepted by final verification")
	}
}

func TestOfflineActivationRechecksAncestorAfterStaging(t *testing.T) {
	data := t.TempDir()
	server := filepath.Join(data, "instance", "server")
	if err := os.MkdirAll(server, 0700); err != nil {
		t.Fatal(err)
	}
	original := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"Inside"}]`
	if err := os.WriteFile(filepath.Join(server, "whitelist.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideServer := filepath.Join(outside, "server")
	if err := os.Mkdir(outsideServer, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideServer, "whitelist.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	pinned, err := openPlayerServer(server)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	swapped := false
	verify := func() error {
		directory, e := pinned.root.Open(".")
		if e != nil {
			return e
		}
		entries, e := directory.ReadDir(-1)
		directory.Close()
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".manager-players-") && !swapped {
				ancestor := filepath.Dir(server)
				if e = os.Rename(ancestor, ancestor+".held"); e != nil {
					return e
				}
				if e = os.Symlink(outside, ancestor); e != nil {
					t.Skip(e)
				}
				swapped = true
			}
		}
		return pinned.verify()
	}
	err = mutateSavedGuarded(t.Context(), pinned.root, console.PlayerMutation{Action: "whitelist_add", UUID: "00000000-0000-0000-0000-000000000002", Name: "Steve"}, time.Now(), verify)
	if !swapped || err == nil {
		t.Fatalf("ancestor substitution before activation escaped verification: swapped=%v err=%v", swapped, err)
	}
	for _, path := range []string{filepath.Join(data, "instance.held", "server", "whitelist.json"), filepath.Join(outsideServer, "whitelist.json")} {
		b, e := os.ReadFile(path)
		if e != nil || string(b) != original {
			t.Fatalf("failed activation changed list %s: %s %v", path, b, e)
		}
	}
	held, e := os.ReadDir(filepath.Join(data, "instance.held", "server"))
	if e != nil {
		t.Fatal(e)
	}
	for _, entry := range held {
		if strings.HasPrefix(entry.Name(), ".manager-players-") {
			t.Fatal("staging leaked after rejected activation")
		}
	}
}

type ancestorSwapNative struct {
	swap  func() error
	calls int
}

func (n *ancestorSwapNative) Identity(_ context.Context, id string) (domain.ProcessIdentity, error) {
	n.calls++
	if n.calls == 1 {
		if err := n.swap(); err != nil {
			return domain.ProcessIdentity{}, err
		}
	}
	return domain.ProcessIdentity{InstanceID: id, PID: 1, StartIdentity: "stable"}, nil
}
func (*ancestorSwapNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{}, nil
}
func (*ancestorSwapNative) WithStopped(_ context.Context, _ string, fn func() error) error {
	return fn()
}

func TestListsServiceRejectsSwapBetweenPathsAndServerOpen(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instances := instance.New(database, paths, &playerStoppedGuard{})
	_, err = instances.Create(t.Context(), domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "test", Name: "Test", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := paths.For("test")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideServer := filepath.Join(outside, "server")
	if err = os.Mkdir(outsideServer, 0700); err != nil {
		t.Fatal(err)
	}
	original := `[{"uuid":"00000000-0000-0000-0000-000000000001","name":"OutsideSecret"}]`
	if err = os.WriteFile(filepath.Join(outsideServer, "whitelist.json"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	native := &ancestorSwapNative{swap: func() error {
		if e := os.Rename(dirs.Root, dirs.Root+".held"); e != nil {
			return e
		}
		if e := os.Symlink(outside, dirs.Root); e != nil {
			t.Skip(e)
		}
		return nil
	}}
	queue := jobs.New(database, nil)
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	service := New(database, paths, instances, nil, queue, native)
	list, err := service.Lists(t.Context(), "test", "whitelist")
	if err == nil || len(list.Entries) != 0 {
		t.Fatalf("service exposed an outside list after validated-path substitution: %+v %v", list, err)
	}
}
