//go:build linux

package supervisor

import (
	"context"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestLinuxControlRejectsDifferentDataRoot(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	err = s.unitControl(context.Background(), "boundary-test", "start")
	if err == nil || domain.AsError(err).Code != domain.CodeUnavailable || !strings.Contains(err.Error(), "data directory differs") {
		t.Fatalf("wrong data root did not fail before privileged unit control: %v", err)
	}
}
func TestLinuxLoadedUnitMustBindInstalledRootAndExecutable(t *testing.T) {
	good := UnitStatus{State: domain.StateStopped, LoadState: "loaded", FragmentPath: "/etc/systemd/system/minecraft@.service", ExecStart: "{ path=/usr/local/bin/minecraft-ui ; argv[]=/usr/local/bin/minecraft-ui launch --data-dir /var/lib/minecraft-ui --instance test --stop-timeout 90s ; ignore_errors=no ; start_time=[n/a] ; stop_time=[n/a] ; pid=0 ; code=(null) ; status=0/0 }", User: "minecraft-ui", Group: "minecraft-ui", Restart: "no", KillMode: "mixed", PrivateUsers: "yes"}
	if err := validateUnitBinding("test", good); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*UnitStatus){
		func(s *UnitStatus) {
			s.ExecStart = strings.ReplaceAll(s.ExecStart, "/var/lib/minecraft-ui", "/production/other")
		},
		func(s *UnitStatus) {
			s.ExecStart = strings.ReplaceAll(s.ExecStart, "--instance test", "--instance another")
		},
		func(s *UnitStatus) {
			s.ExecStart = strings.ReplaceAll(s.ExecStart, "/usr/local/bin/minecraft-ui", "/bin/sh")
		},
		func(s *UnitStatus) { s.DropInPaths = "/etc/systemd/system/minecraft@test.service.d/override.conf" },
		func(s *UnitStatus) { s.FragmentPath = "/etc/systemd/system/minecraft@test.service" },
		func(s *UnitStatus) { s.User = "root" },
		func(s *UnitStatus) { s.Restart = "always" },
	} {
		bad := good
		change(&bad)
		if err := validateUnitBinding("test", bad); err == nil {
			t.Fatal("unsafe loaded service binding accepted", bad)
		}
	}
}
