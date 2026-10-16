package console

import (
	"context"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"os"
	"testing"
)

type repairNative struct {
	identity   domain.ProcessIdentity
	path       string
	releaseErr bool
	commands   []string
	watchdog   string
}

func (f *repairNative) Identity(context.Context, string) (domain.ProcessIdentity, error) {
	return f.identity, nil
}
func (f *repairNative) Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error) {
	return supervisor.Observation{State: domain.StateRunning, ProcessRunning: true, WatchdogError: f.watchdog}, nil
}
func (f *repairNative) Command(_ context.Context, id domain.ProcessIdentity, command string) error {
	f.commands = append(f.commands, command)
	if id != f.identity {
		return errors.New("stale")
	}
	file, err := os.OpenFile(f.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString("[12:00:00] [Server thread/INFO]: Automatic saving is now enabled\n")
	return err
}
func (f *repairNative) ReleaseSaveLease(ctx context.Context, id domain.ProcessIdentity, lease string) error {
	if f.releaseErr {
		return errors.New("absent lease")
	}
	return f.Command(ctx, id, "save-on")
}
func TestSavingRecoveryRequiresPausedAdmissionAndFreshExactIdentity(t *testing.T) {
	h, path := logFixture(t)
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	native := &repairNative{identity: domain.ProcessIdentity{InstanceID: "test", PID: 123, StartIdentity: "generation"}, path: path, watchdog: "old delivery failure"}
	q := jobs.New(h.db, nil)
	s := New(h.db, h.paths, native, fixtureInstances{instance.Detail{Instance: domain.Instance{ID: "test", MinecraftVersion: "1.16.5"}}}, q)
	if _, err := s.RecoverSaving(t.Context(), native.identity, "lease"); err == nil {
		t.Fatal("accepted before paused startup")
	}
	if err := q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	stale := native.identity
	stale.StartIdentity = "other"
	if _, err := s.RecoverSaving(t.Context(), stale, "lease"); err == nil {
		t.Fatal("accepted stale process")
	}
	result, err := s.RecoverSaving(t.Context(), native.identity, "lease")
	if err != nil || !result.Verified {
		t.Fatalf("recovery failed: %+v %v", result, err)
	}
	native.releaseErr = true
	result, err = s.RecoverSaving(t.Context(), native.identity, "lease")
	if err == nil || !result.Verified {
		t.Fatalf("must distinguish fresh saving from unresolved lease: %+v %v", result, err)
	}
	for _, command := range native.commands {
		if command != "save-on" {
			t.Fatalf("unexpected recovery command %q", command)
		}
	}
	if err := q.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecoverSaving(t.Context(), native.identity, "lease"); err == nil {
		t.Fatal("accepted outside startup")
	}
}

func TestSavingRecoveryAttemptsDeliveryWithoutCursor(t *testing.T) {
	h, path := logFixture(t)
	native := &repairNative{identity: domain.ProcessIdentity{InstanceID: "test", PID: 123, StartIdentity: "generation"}, path: path}
	q := jobs.New(h.db, nil)
	s := New(h.db, h.paths, native, fixtureInstances{instance.Detail{Instance: domain.Instance{ID: "test", MinecraftVersion: "1.16.5"}}}, q)
	if err := q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	_, err := s.RecoverSaving(t.Context(), native.identity, "lease")
	if err == nil || len(native.commands) == 0 {
		t.Fatalf("missing cursor skipped saving attempt: %v %v", native.commands, err)
	}
}
