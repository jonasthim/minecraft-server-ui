//go:build linux

package supervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const unitHelper = "/usr/local/lib/minecraft-ui/unitctl"

func UnitName(id string) (string, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return "", err
	}
	return "minecraft@" + id + ".service", nil
}
func (s *Supervisor) unitControl(ctx context.Context, id, action string) error {
	if _, err := UnitName(id); err != nil {
		return err
	}
	if err := s.configBinding(); err != nil {
		return err
	}
	switch action {
	case "start", "stop", "enable", "disable":
	default:
		return fmt.Errorf("unit action is not allowed")
	}
	if _, err := s.UnitStatus(ctx, id); err != nil {
		return err
	}
	// Every executable path is fixed. Never fall back to PATH or accept custom
	// sudo/systemctl/command paths from writable manager configuration.
	if err := exec.CommandContext(ctx, "/usr/bin/sudo", "-n", unitHelper, action, id).Run(); err != nil {
		return fmt.Errorf("unit control %s: %w", action, err)
	}
	return nil
}
func (s *Supervisor) UnitStatus(ctx context.Context, id string) (UnitStatus, error) {
	if err := s.configBinding(); err != nil {
		return UnitStatus{State: domain.StateUnknown}, err
	}
	unit, err := UnitName(id)
	if err != nil {
		return UnitStatus{}, err
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", unit, "--property=LoadState,ActiveState,SubState,Result,UnitFileState,FragmentPath,DropInPaths,ExecStart,User,Group,Restart,KillMode").Output()
	if err != nil {
		return UnitStatus{State: domain.StateUnknown}, fmt.Errorf("query instance unit: %w", err)
	}
	status := parseSystemd(string(out))
	if err = validateUnitBinding(id, status); err != nil {
		return UnitStatus{State: domain.StateUnknown}, err
	}
	return status, nil
}
func (s *Supervisor) platformRequireStopped(ctx context.Context, id string) error {
	status, err := s.UnitStatus(ctx, id)
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "native systemd lifecycle verification unavailable", err)
	}
	switch status.State {
	case domain.StateStopped, domain.StateCrashed:
		return nil
	case domain.StateUnknown:
		return domain.E(domain.CodeUnavailable, "instance unit state is unknown")
	default:
		return domain.E(domain.CodeInstanceRunning, "instance unit is active")
	}
}
func (s *Supervisor) platformStart(ctx context.Context, id string) error {
	return s.unitControl(ctx, id, "start")
}
func (s *Supervisor) platformSetAutostart(ctx context.Context, id string, enabled bool) error {
	action := "disable"
	if enabled {
		action = "enable"
	}
	return s.unitControl(ctx, id, action)
}
func (s *Supervisor) platformAutostart(context.Context, map[string]domain.LaunchSpec) error {
	return nil
}                                                            // systemd boot owns autostart; manager restarts leave deliberate stops alone
func (s *Supervisor) platformShutdown(context.Context) error { return nil } // independent systemd units survive manager restart

// The shipped installation is deliberately fixed. Custom root-owned deployments
// require a separately reviewed binding contract; a dev manager must never target
// the production unit namespace just because an instance ID happens to match.
func (s *Supervisor) configBinding() error {
	if filepath.Clean(s.cfg.DataDir) != "/var/lib/minecraft-ui" {
		return domain.E(domain.CodeUnavailable, "configured data directory differs from fixed systemd installation")
	}
	if filepath.Clean(s.cfg.Executable) != "/usr/local/bin/minecraft-ui" {
		return domain.E(domain.CodeUnavailable, "manager executable differs from fixed systemd installation")
	}
	for _, path := range []string{"/usr/local/bin/minecraft-ui", unitHelper, "/etc/systemd/system/minecraft@.service"} {
		if err := rootOwnedPath(path); err != nil {
			return domain.Wrap(domain.CodeUnavailable, "native systemd installation is missing or unsafe", err)
		}
	}
	return nil
}
func rootOwnedPath(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		fi, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("native installation path must be root-owned and not writable by group/others: %s", current)
		}
		if current == path && !fi.Mode().IsRegular() {
			return fmt.Errorf("native installation file must be regular")
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}
func validateUnitBinding(id string, status UnitStatus) error {
	if status.LoadState != "loaded" || status.FragmentPath != "/etc/systemd/system/minecraft@.service" || status.DropInPaths != "" {
		return domain.E(domain.CodeUnavailable, "instance unit is uninstalled or has an unrecognized installation binding")
	}
	expected := "/usr/local/bin/minecraft-ui launch --data-dir /var/lib/minecraft-ui --instance " + id + " --stop-timeout 90s"
	_, tail, ok := strings.Cut(status.ExecStart, "argv[]=")
	argv, _, end := strings.Cut(tail, " ; ignore_errors=")
	if !ok || !end || argv != expected || !strings.HasPrefix(status.ExecStart, "{ path=/usr/local/bin/minecraft-ui ; ") || strings.Count(status.ExecStart, "{ path=") != 1 || status.User != "minecraft-ui" || status.Group != "minecraft-ui" || status.Restart != "no" || status.KillMode != "mixed" {
		return domain.E(domain.CodeUnavailable, "loaded instance unit does not match the fixed native launch contract")
	}
	return nil
}
