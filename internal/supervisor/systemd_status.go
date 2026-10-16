package supervisor

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"strconv"
	"strings"
)

type UnitStatus struct {
	FragmentPath, DropInPaths, ExecStart, User, Group, Restart, KillMode string
	PrivateUsers, ControlGroup, InvocationID                             string
	MainPID                                                              int
	State                                                                domain.InstanceState
	Result                                                               string
	Autostart                                                            bool
	LoadState                                                            string
}

func parseSystemd(out string) UnitStatus {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			props[key] = value
		}
	}
	mainPID, _ := strconv.Atoi(props["MainPID"])
	status := UnitStatus{PrivateUsers: props["PrivateUsers"], ControlGroup: props["ControlGroup"], InvocationID: props["InvocationID"], MainPID: mainPID, State: domain.StateUnknown, Result: props["Result"], LoadState: props["LoadState"], FragmentPath: props["FragmentPath"], DropInPaths: props["DropInPaths"], ExecStart: props["ExecStart"], User: props["User"], Group: props["Group"], Restart: props["Restart"], KillMode: props["KillMode"]}
	if props["LoadState"] != "loaded" {
		return status
	}
	switch props["ActiveState"] {
	case "active":
		status.State = domain.StateRunning
	case "activating":
		status.State = domain.StateStarting
	case "deactivating":
		status.State = domain.StateStopping
	case "inactive":
		status.State = domain.StateStopped
	case "failed":
		status.State = domain.StateCrashed
	}
	status.Autostart = props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime"
	return status
}
