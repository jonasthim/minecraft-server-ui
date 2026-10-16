//go:build linux

package supervisor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
)

// TestSystemdHostLifecycle is destructive only to the explicitly designated test
// instance. It requires the installed root-owned unit/helper and a pre-provisioned
// real Java server. It never downloads artifacts, changes game versions or worlds.
func TestSystemdHostLifecycle(t *testing.T) {
	if os.Getenv("MINECRAFT_UI_SYSTEMD_SMOKE") != "1" {
		t.Skip("opt-in real systemd host qualification; no installed native service contract qualified in this session")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Fatal("systemd execution environment unavailable", err)
	}
	id := os.Getenv("MINECRAFT_UI_SYSTEMD_SMOKE_INSTANCE")
	if id == "" {
		t.Fatal("explicit dedicated test instance required")
	}
	const dataDir = "/var/lib/minecraft-ui"
	p, err := launcher.Paths(dataDir, id)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Spec domain.LaunchSpec `json:"spec"`
		Env  map[string]string `json:"env"`
	}
	if err = launcher.ReadJSON(filepath.Join(p.State, "launch.json"), &stored); err != nil {
		t.Fatal("pre-provision a real Java server launch specification", err)
	}
	stored.Spec.Env = stored.Env
	if filepath.Base(stored.Spec.Executable) != "java" || stored.Spec.JavaRuntimeID == "" || stored.Spec.JavaChecksum.Value == "" {
		t.Fatal("real managed JVM provenance required")
	}
	s, err := New(Config{DataDir: dataDir, Executable: "/usr/local/bin/minecraft-ui"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	status, err := s.UnitStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if status.State != domain.StateStopped && status.State != domain.StateCrashed {
		t.Fatal("test instance must initially be stopped")
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 110*time.Second)
		defer c()
		_ = s.SetAutostart(cleanup, id, status.Autostart)
		if identity, e := s.Identity(cleanup, id); e == nil {
			_ = s.Stop(cleanup, identity)
		}
	}()
	if err = s.SetAutostart(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	first, err := s.Start(ctx, id, stored.Spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("started JVM pid=%d launcher=%d start=%s runtime=%s hash=%s", first.PID, first.LauncherPID, first.StartIdentity, first.JavaRuntimeID, first.JavaChecksum.Value)
	// The ordinary test proves fresh supervisor reconnect, not manager restart.
	second, err := New(Config{DataDir: dataDir, Executable: "/usr/local/bin/minecraft-ui"})
	if err != nil {
		t.Fatal(err)
	}
	if state, e := second.Status(ctx, first); e != nil || state != domain.StateRunning {
		t.Fatal("fresh supervisor failed to reconnect", state, e)
	}
	if os.Getenv("MINECRAFT_UI_SYSTEMD_SMOKE_RESTART_MANAGER") == "1" {
		if err = exec.CommandContext(ctx, "/usr/bin/systemctl", "restart", "minecraft-ui.service").Run(); err != nil {
			t.Fatal("explicitly opted-in real manager restart failed", err)
		}
		if err = launcher.VerifyIdentity(first); err != nil {
			t.Fatal("JVM did not survive actual manager service restart", err)
		}
		t.Log("actual manager service restart preserved JVM identity")
	} else {
		t.Log("actual manager restart NOT exercised; set MINECRAFT_UI_SYSTEMD_SMOKE_RESTART_MANAGER=1 on qualification host")
	}
	if err = second.Stop(ctx, first); err != nil {
		t.Fatal(err)
	}
	observation, err := second.Inspect(ctx, first)
	if err != nil || observation.State != domain.StateStopped || observation.Exit == nil || !observation.Exit.Deliberate {
		t.Fatal("deliberate stop classification", observation, err)
	}
	// Restart=no must keep the deliberately stopped instance down.
	time.Sleep(time.Second)
	if alive, e := launcher.ProbeIdentity(first); e != nil || alive {
		t.Fatal("stopped JVM resumed", alive, e)
	}
}
