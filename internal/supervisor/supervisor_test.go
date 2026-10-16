package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
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
