//go:build linux

package supervisor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"golang.org/x/sys/unix"
)

type inspectionScope struct {
	LauncherPID, JVMPID, JVMParent                      int
	UnitGroup, LauncherGroup, JVMGroup                  string
	InspectorNamespace, LauncherNamespace, JVMNamespace string
	NamespaceOwnerUID                                   uint32
}

func validateInspectionScope(s inspectionScope) error {
	if s.LauncherPID <= 0 || s.JVMPID <= 0 || s.JVMPID == s.LauncherPID || s.JVMParent != s.LauncherPID || s.UnitGroup == "" || s.LauncherGroup != s.UnitGroup || s.JVMGroup != s.UnitGroup {
		return fmt.Errorf("JVM is not the direct child of the loaded unit launcher")
	}
	if s.InspectorNamespace == "" || s.LauncherNamespace == "" || s.JVMNamespace != s.LauncherNamespace || s.LauncherNamespace == s.InspectorNamespace || s.NamespaceOwnerUID != 0 {
		return fmt.Errorf("root-created private user namespace boundary is not active")
	}
	return nil
}

// NativeInspect is the only privileged read entrypoint. Its sole external input
// is an instance ID; PID, cgroup, executable and paths are all derived internally.
// It performs no signals, process creation, file mutation, or namespace entry.
func NativeInspect(ctx context.Context, id string) (launcher.NativeInspection, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return launcher.NativeInspection{}, err
	}
	if os.Geteuid() != 0 {
		return launcher.NativeInspection{}, fmt.Errorf("native inspection requires the fixed privileged broker")
	}
	supervisor, err := New(Config{DataDir: "/var/lib/minecraft-ui", Executable: "/usr/local/bin/minecraft-ui"})
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	status, err := supervisor.UnitStatus(ctx, id)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	if status.State != domain.StateRunning && status.State != domain.StateStopping {
		return launcher.NativeInspection{}, fmt.Errorf("instance unit has no active JVM")
	}
	group := status.ControlGroup
	unit, _ := UnitName(id)
	if status.MainPID <= 0 || len(status.InvocationID) != 32 || !filepath.IsAbs(group) || filepath.Clean(group) != group || filepath.Base(group) != unit || group == "/" {
		return launcher.NativeInspection{}, fmt.Errorf("loaded unit identity is incomplete")
	}
	launcherFD, err := unix.PidfdOpen(status.MainPID, 0)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	defer unix.Close(launcherFD)
	ownNS, _, err := processNamespace(os.Getpid())
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	launcherNS, owner, err := processNamespace(status.MainPID)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	launcherGroup, err := processGroup(status.MainPID)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	if launcherNS == ownNS || owner != 0 || launcherGroup != group {
		return launcher.NativeInspection{}, fmt.Errorf("loaded launcher isolation is absent or mismatched")
	}
	if err = launcher.VerifyMountIsolation(status.MainPID, id); err != nil {
		return launcher.NativeInspection{}, err
	}
	cgroups, err := os.OpenRoot("/sys/fs/cgroup")
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	defer cgroups.Close()
	file, err := cgroups.Open(strings.TrimPrefix(group, "/") + "/cgroup.procs")
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	raw, err := boundedRead(file, 64<<10)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) > 4096 {
		return launcher.NativeInspection{}, fmt.Errorf("instance unit has too many processes")
	}
	candidate := 0
	for _, field := range fields {
		if err = ctx.Err(); err != nil {
			return launcher.NativeInspection{}, err
		}
		pid, e := strconv.Atoi(field)
		if e != nil || pid <= 0 {
			return launcher.NativeInspection{}, fmt.Errorf("invalid cgroup process identity")
		}
		if pid == status.MainPID {
			continue
		}
		parent, e := processParent(pid)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return launcher.NativeInspection{}, e
		}
		if parent == status.MainPID {
			if candidate != 0 {
				return launcher.NativeInspection{}, fmt.Errorf("ambiguous direct JVM children")
			}
			candidate = pid
		}
	}
	if candidate == 0 {
		return launcher.NativeInspection{}, fmt.Errorf("unit launcher has no direct JVM child")
	}
	jvmFD, err := unix.PidfdOpen(candidate, 0)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	defer unix.Close(jvmFD)
	parent, err := processParent(candidate)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	jvmGroup, err := processGroup(candidate)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	jvmNS, _, err := processNamespace(candidate)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	if err = validateInspectionScope(inspectionScope{LauncherPID: status.MainPID, JVMPID: candidate, JVMParent: parent, UnitGroup: group, LauncherGroup: launcherGroup, JVMGroup: jvmGroup, InspectorNamespace: ownNS, LauncherNamespace: launcherNS, JVMNamespace: jvmNS, NamespaceOwnerUID: owner}); err != nil {
		return launcher.NativeInspection{}, err
	}
	result, err := launcher.ReadNativeInspection(id, candidate, status.MainPID)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	again, err := supervisor.UnitStatus(ctx, id)
	if err != nil {
		return launcher.NativeInspection{}, err
	}
	if again.MainPID != status.MainPID || again.ControlGroup != group || again.InvocationID != status.InvocationID {
		return launcher.NativeInspection{}, fmt.Errorf("unit changed during identity inspection")
	}
	for _, fd := range []int{launcherFD, jvmFD} {
		events := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, e := unix.Poll(events, 0)
		if e != nil {
			return launcher.NativeInspection{}, e
		}
		if n != 0 {
			return launcher.NativeInspection{}, fmt.Errorf("process exited during identity inspection")
		}
	}
	result.UserNamespace = jvmNS
	result.InvocationID = status.InvocationID
	return result, nil
}
func boundedRead(file *os.File, limit int64) ([]byte, error) {
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("native metadata exceeds limit")
	}
	return data, nil
}
func procRead(pid int, name string, limit int64) ([]byte, error) {
	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), name))
	if err != nil {
		return nil, err
	}
	return boundedRead(file, limit)
}
func processParent(pid int) (int, error) {
	raw, err := procRead(pid, "stat", 64<<10)
	if err != nil {
		return 0, err
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 {
		return 0, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 2 {
		return 0, fmt.Errorf("incomplete process stat")
	}
	return strconv.Atoi(fields[1])
}
func processGroup(pid int) (string, error) {
	raw, err := procRead(pid, "cgroup", 64<<10)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			return strings.TrimPrefix(line, "0::"), nil
		}
	}
	return "", fmt.Errorf("unified cgroup identity unavailable")
}
func processNamespace(pid int) (string, uint32, error) {
	file, err := os.Open(filepath.Join("/proc", strconv.Itoa(pid), "ns/user"))
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, fmt.Errorf("namespace inode unavailable")
	}
	owner, err := unix.IoctlGetUint32(int(file.Fd()), unix.NS_GET_OWNER_UID)
	if err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), owner, nil
}
