package launcher

import (
	"fmt"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// NativeInspection is the bounded read-only broker result. No environment,
// command line, credentials or caller-selected file contents are returned.
type NativeInspection struct {
	InstanceID    string `json:"instance_id"`
	PID           int    `json:"pid"`
	LauncherPID   int    `json:"launcher_pid"`
	StartIdentity string `json:"start_identity"`
	Executable    string `json:"executable"`
	WorkingDir    string `json:"working_dir"`
	UserNamespace string `json:"user_namespace"`
	InvocationID  string `json:"invocation_id"`
}

// ReadNativeInspection only reads native process metadata. The root broker must
// establish the unit/parent/cgroup/namespace scope before calling this function.
func ReadNativeInspection(id string, pid, launcherPID int) (NativeInspection, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return NativeInspection{}, err
	}
	start, exe, cwd, err := processIdentity(pid)
	if err != nil {
		return NativeInspection{}, fmt.Errorf("read scoped JVM identity: %w", err)
	}
	return NativeInspection{InstanceID: id, PID: pid, LauncherPID: launcherPID, StartIdentity: start, Executable: exe, WorkingDir: cwd}, nil
}
