//go:build linux

package launcher

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func validatePrivateMap(raw string, own int) error {
	seen := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			return fmt.Errorf("invalid private identity map")
		}
		inside, e := strconv.Atoi(f[0])
		outside, e2 := strconv.Atoi(f[1])
		count, e3 := strconv.Atoi(f[2])
		if e != nil || e2 != nil || e3 != nil || inside != outside || count != 1 || (inside != 0 && inside != own) || seen[inside] {
			return fmt.Errorf("identity map is not the minimal system-service mapping")
		}
		seen[inside] = true
	}
	if own <= 0 || len(seen) != 2 || !seen[0] || !seen[own] {
		return fmt.Errorf("private identity map is absent")
	}
	return nil
}

// VerifyMountIsolation reads only the selected process's mount table. The root
// broker derives that PID from the loaded fixed unit before calling this.
func VerifyMountIsolation(pid int, id string) error {
	if _, err := Paths("/var/lib/minecraft-ui", id); err != nil {
		return err
	}
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/mountinfo")
	if err != nil {
		return err
	}
	if len(raw) > 2<<20 {
		return fmt.Errorf("mount table exceeds bound")
	}
	return validateIsolationMounts(string(raw), id)
}
func validateIsolationMounts(raw, id string) error {
	root := "/var/lib/minecraft-ui/instances/" + id
	required := map[string]string{"/var/lib/minecraft-ui": "ro", "/var/lib/minecraft-ui/runtimes": "ro", root: "ro", root + "/server": "rw", root + "/logs": "rw", root + "/state": "rw", root + "/server/.manager": "ro", root + "/state/launch.json": "ro", "/var/lib/minecraft-ui/locks/" + id + ".lock": "rw"}
	found := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		if mode, ok := required[f[4]]; ok {
			found[f[4]] = strings.Contains(","+f[5]+",", ","+mode+",")
			if f[4] == "/var/lib/minecraft-ui" {
				_, tail, ok := strings.Cut(line, " - ")
				types := strings.Fields(tail)
				found[f[4]] = found[f[4]] && ok && len(types) > 0 && types[0] == "tmpfs"
			}
		}
	}
	for path := range required {
		if !found[path] {
			return fmt.Errorf("required pinned mount mode is absent: %s", path)
		}
	}
	return nil
}

// This is checked in the actual launcher before exec, including systemd boot.
// Unsupported namespace/mount options fail closed instead of reducing isolation.
func verifyProcessIsolation(cfg Config) error {
	if cfg.DataDir != "/var/lib/minecraft-ui" {
		return fmt.Errorf("Linux launch requires the fixed systemd installation")
	}
	for _, item := range []struct {
		name string
		own  int
	}{{"uid_map", os.Geteuid()}, {"gid_map", os.Getegid()}} {
		raw, err := os.ReadFile("/proc/self/" + item.name)
		if err != nil {
			return err
		}
		if err = validatePrivateMap(string(raw), item.own); err != nil {
			return err
		}
	}
	ns, err := os.Open("/proc/self/ns/user")
	if err != nil {
		return err
	}
	defer ns.Close()
	owner, err := unix.IoctlGetUint32(int(ns.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil {
		return fmt.Errorf("inspect namespace owner: %w", err)
	}
	if owner != 0 {
		return fmt.Errorf("private namespace must be created by the system manager")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(status), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			props[key] = strings.TrimSpace(value)
		}
	}
	caps, err := strconv.ParseUint(props["CapEff"], 16, 64)
	if err != nil || caps != 0 || props["NoNewPrivs"] != "1" {
		return fmt.Errorf("launcher retains privileges or lacks NoNewPrivileges")
	}
	return VerifyMountIsolation(os.Getpid(), cfg.InstanceID)
}
