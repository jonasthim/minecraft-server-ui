package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func configActivationFixture(t *testing.T) (*Service, domain.User, *testNative, instance.Draft, string) {
	t.Helper()
	s, u, n := fixture(t)
	paths, _ := instance.NewPaths(s.dataDir)
	s.instances = instance.New(s.db, paths, testGuard{n}, instance.WithJobMutationGuard(testGuard{n}), instance.WithActivationCoordinator(s))
	d, _ := s.instances.Get(t.Context(), "test")
	plan, _ := testProvider{}.Resolve(t.Context(), selection(d))
	old := installation{Generation: "old", Plan: plan, Spec: domain.LaunchSpec{JavaRuntimeID: "runtime", Args: []string{"-Xms512M", "-Xmx1024M", "-jar", "immutable.jar"}}}
	if err := s.saveInstallation(t.Context(), "test", old); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dataDir, "instances", "test", "state", "launch.json")
	if err := os.WriteFile(path, []byte("-Xmx1024M"), 0600); err != nil {
		t.Fatal(err)
	}
	s.seal = func(_ context.Context, _ string, spec domain.LaunchSpec) error {
		return os.WriteFile(path, []byte(spec.Args[1]), 0600)
	}
	draft := instance.Draft{Instance: d.Instance, Config: d.Config}
	draft.Instance.MemoryMiB = 4096
	draft.Instance.JavaRuntimeID = ""
	return s, u, n, draft, path
}
func TestStoppedMemoryEditSealsNextBootInSameCommit(t *testing.T) {
	s, u, _, draft, path := configActivationFixture(t)
	if _, err := s.db.Exec(`INSERT INTO lifecycle_autostart(instance_id,desired,applied) VALUES('test',1,1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.instances.Update(t.Context(), u, "test", draft); err != nil {
		t.Fatal(err)
	}
	current, err := s.loadInstallation(t.Context(), "test")
	if err != nil || current.Spec.Args[1] != "-Xmx4096M" {
		t.Fatalf("activation=%+v %v", current, err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != "-Xmx4096M" {
		t.Fatalf("boot memory=%s %v", bytes, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err != nil {
		t.Fatalf("successful edit blocked boot: %v", err)
	}
	state, err := s.State(t.Context(), "test")
	if err != nil || !state.AutostartDesired || state.AutostartApplied == nil || !*state.AutostartApplied {
		t.Fatalf("memory edit changed native boot policy: %+v %v", state, err)
	}
}
func TestMemoryEditBeforeAfterSealAndCancellationRollBackTogether(t *testing.T) {
	for _, failure := range []string{"before_seal", "after_seal", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			s, u, _, draft, path := configActivationFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			originalSeal := s.seal
			s.seal = func(ctx context.Context, id string, spec domain.LaunchSpec) error {
				if spec.Args[1] == "-Xmx4096M" {
					if failure == "before_seal" {
						return errors.New("seal rejected")
					}
					if err := originalSeal(ctx, id, spec); err != nil {
						return err
					}
					if failure == "cancel" {
						cancel()
						return ctx.Err()
					}
				}
				return originalSeal(ctx, id, spec)
			}
			if failure == "after_seal" {
				if _, err := s.db.Exec(`CREATE TRIGGER reject_memory_activation BEFORE UPDATE ON lifecycle_installations BEGIN SELECT RAISE(ABORT,'reject installation commit'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.instances.Update(ctx, u, "test", draft); err == nil {
				t.Fatal("injected failure hidden")
			}
			d, err := s.instances.Get(t.Context(), "test")
			if err != nil || d.Instance.MemoryMiB != 1024 {
				t.Fatalf("DB memory changed: %+v %v", d, err)
			}
			current, err := s.loadInstallation(t.Context(), "test")
			if err != nil || current.Spec.Args[1] != "-Xmx1024M" {
				t.Fatalf("installation changed: %+v %v", current, err)
			}
			bytes, err := os.ReadFile(path)
			if err != nil || string(bytes) != "-Xmx1024M" {
				t.Fatalf("boot changed: %s %v", bytes, err)
			}
			if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err != nil {
				t.Fatalf("verified rollback blocked boot: %v", err)
			}
		})
	}
}
func TestMemoryEditCleanupFailureRetainsBootGuard(t *testing.T) {
	s, u, _, draft, _ := configActivationFixture(t)
	s.seal = func(context.Context, string, domain.LaunchSpec) error { return errors.New("seal unavailable") }
	_, err := s.instances.Update(t.Context(), u, "test", draft)
	if err == nil || !strings.Contains(err.Error(), "activation pending") {
		t.Fatalf("partial cleanup unclear: %v", err)
	}
	if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err == nil {
		t.Fatal("unverified recovery allowed boot")
	}
	var pending int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM lifecycle_activations`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("journal missing: %d %v", pending, err)
	}
}

type interruptedFinish struct{ instance.ConfigActivation }

func (a interruptedFinish) Finish(context.Context) error {
	return errors.New("interrupted after DB commit")
}

type interruptedCoordinator struct{ service *Service }

func (c interruptedCoordinator) PrepareConfigActivation(ctx context.Context, id string, memory int) (instance.ConfigActivation, error) {
	activation, err := c.service.PrepareConfigActivation(ctx, id, memory)
	if err != nil {
		return nil, err
	}
	return interruptedFinish{activation}, nil
}
func TestMemoryEditCrashAfterCommitRecoversNewBootMemory(t *testing.T) {
	s, u, n, draft, path := configActivationFixture(t)
	paths, _ := instance.NewPaths(s.dataDir)
	s.instances = instance.New(s.db, paths, testGuard{n}, instance.WithActivationCoordinator(interruptedCoordinator{s}))
	_, err := s.instances.Update(t.Context(), u, "test", draft)
	if err == nil || !strings.Contains(err.Error(), "activation pending") {
		t.Fatalf("committed interruption not reported: %v", err)
	}
	current, err := s.instances.Get(t.Context(), "test")
	if err != nil || current.Instance.MemoryMiB != 4096 {
		t.Fatalf("committed memory not preserved: %+v %v", current, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err == nil {
		t.Fatal("interrupted completion allowed boot")
	}
	if err = s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != "-Xmx4096M" {
		t.Fatalf("recovery rewound committed memory: %s %v", bytes, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err != nil {
		t.Fatal(err)
	}
}
func TestPreparedMemoryEditCrashBeforeConfigJournalRecoversOldBootMemory(t *testing.T) {
	s, _, _, _, path := configActivationFixture(t)
	if _, err := s.PrepareConfigActivation(t.Context(), "test", 4096); err != nil {
		t.Fatal(err)
	}
	if err := launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err == nil {
		t.Fatal("prepared edit did not block boot")
	}
	if err := s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || string(bytes) != "-Xmx1024M" {
		t.Fatalf("uncommitted edit changed boot: %s %v", bytes, err)
	}
	if err = launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err != nil {
		t.Fatal(err)
	}
}
func TestConfigRollbackFailureRetainsBootGuardAndBothRecoveryJournals(t *testing.T) {
	s, u, _, draft, _ := configActivationFixture(t)
	journal := filepath.Join(s.dataDir, "instances", "test", "state", "config.pending.json")
	s.seal = func(context.Context, string, domain.LaunchSpec) error {
		if err := os.Remove(journal); err != nil {
			return err
		}
		if err := os.Mkdir(journal, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(journal, "unexpected"), []byte("hold"), 0600); err != nil {
			return err
		}
		return errors.New("seal interrupted")
	}
	if _, err := s.instances.Update(t.Context(), u, "test", draft); err == nil {
		t.Fatal("rollback failure hidden")
	}
	if err := launcher.CheckActivationReady(t.Context(), s.dataDir, "test"); err == nil {
		t.Fatal("failed config restoration allowed boot")
	}
	if err := s.Reconcile(t.Context()); err == nil {
		t.Fatal("lifecycle recovery ignored unresolved config journal")
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatal("config journal unexpectedly removed", err)
	}
}
