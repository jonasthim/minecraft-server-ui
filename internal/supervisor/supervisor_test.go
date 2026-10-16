package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

func TestNewRejectsRelativePaths(t *testing.T) {
	if _, err := New(Config{DataDir: "relative"}); err == nil {
		t.Fatal("relative data root accepted")
	}
}
func TestCancelledStartNeverWritesSpec(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Start(ctx, "test", domain.LaunchSpec{})
	if err == nil {
		t.Fatal("canceled start accepted")
	}
	if _, err = os.Stat(filepath.Join(root, "instances", "test", "state", "launch.json")); !os.IsNotExist(err) {
		t.Fatal("canceled start mutated disk", err)
	}
}
func TestInvalidIDNeverReachesUnitControl(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetAutostart(context.Background(), "../manager", true); err == nil {
		t.Fatal("unsafe id")
	}
}
func TestParseSystemdUnknownFailsClosed(t *testing.T) {
	if got := parseSystemd("ActiveState=surprising\nLoadState=loaded\n"); got.State != domain.StateUnknown {
		t.Fatal(got)
	}
	if got := parseSystemd("ActiveState=failed\nLoadState=loaded\nResult=timeout\n"); got.State != domain.StateCrashed || got.Result != "timeout" {
		t.Fatal(got)
	}
}

func TestShutdownRejectsNewStarts(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = s.Start(context.Background(), "test", domain.LaunchSpec{})
	if err == nil || domain.AsError(err).Code != domain.CodeUnavailable {
		t.Fatal("start admitted after shutdown", err)
	}
}

func staleIdentity(t *testing.T) domain.ProcessIdentity {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	identity, err := launcher.CaptureIdentity("test", os.Getpid(), domain.LaunchSpec{Executable: exe, WorkingDir: cwd})
	if err != nil {
		t.Fatal(err)
	}
	identity.StartIdentity += "previous"
	return identity
}
func TestAlreadyDeadStopWaitsForLauncherCompletion(t *testing.T) {
	root := t.TempDir()
	s, err := New(Config{DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	identity := staleIdentity(t)
	release, err := launcher.AcquireAdmission(root, identity.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err = s.Stop(ctx, identity); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("dead JVM bypassed active launcher completion: %v", err)
	}
}
func TestAlreadyDeadStopPreservesFinalFailure(t *testing.T) {
	for _, kind := range []string{"forced", "recovery", "missing", "crashed", "clean"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			s, err := New(Config{DataDir: root})
			if err != nil {
				t.Fatal(err)
			}
			identity := staleIdentity(t)
			p, err := launcher.Paths(root, identity.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(p.State, 0700); err != nil {
				t.Fatal(err)
			}
			final := launcher.Exit{Identity: identity, Deliberate: true, ExitedAt: time.Now().UTC()}
			switch kind {
			case "forced":
				final.Forced = true
			case "recovery":
				final.WatchdogError = "save-on failed"
			case "crashed":
				final.ExitCode = 2
				final.Deliberate = false
			}
			if kind != "missing" {
				if err = launcher.WriteJSON(filepath.Join(p.State, "exit.json"), final); err != nil {
					t.Fatal(err)
				}
			}
			err = s.Stop(context.Background(), identity)
			if kind == "clean" && err != nil {
				t.Fatal(err)
			}
			if kind != "clean" && err == nil {
				t.Fatalf("already dead Stop hid %s failure", kind)
			}
		})
	}
}
