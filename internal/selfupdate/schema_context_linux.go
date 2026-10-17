//go:build linux

package selfupdate

import (
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const linuxProbeProcReadLimit = 16 * 1024

type linuxProbeStatus struct {
	euid         string
	capEffective string
	capPermitted string
	capBounding  string
	noNewPrivs   string
	seccomp      string
}

type linuxProbeContext struct {
	process linuxProbeStatus
	thread  linuxProbeStatus
	uidMap  string
}

// startLinuxProbeCommand captures process and calling-thread state and starts
// the child while pinned to that same OS thread. Linux credentials are per
// thread, so the thread-self record is the state inherited by exec.
func startLinuxProbeCommand(command *exec.Cmd) (linuxProbeContext, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx := captureLinuxProbeContext()
	err := command.Start()
	return ctx, err
}

func captureLinuxProbeContext() linuxProbeContext {
	process, okProcess := readLinuxProbeFile("/proc/self/status", linuxProbeProcReadLimit)
	thread, okThread := readLinuxProbeFile("/proc/thread-self/status", linuxProbeProcReadLimit)
	uidMap, okUIDMap := readLinuxProbeFile("/proc/thread-self/uid_map", 1024)
	result := linuxProbeContext{
		process: parseLinuxProbeStatus(process),
		thread:  parseLinuxProbeStatus(thread),
		uidMap:  parseLinuxProbeUIDMap(uidMap),
	}
	if !okProcess {
		result.process = unknownLinuxProbeStatus()
	}
	if !okThread {
		result.thread = unknownLinuxProbeStatus()
	}
	if !okUIDMap {
		result.uidMap = "unknown"
	}
	return result
}

func readLinuxProbeFile(path string, limit int) ([]byte, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	return body, err == nil && len(body) <= limit
}

func unknownLinuxProbeStatus() linuxProbeStatus {
	return linuxProbeStatus{euid: "unknown", capEffective: "unknown", capPermitted: "unknown",
		capBounding: "unknown", noNewPrivs: "unknown", seccomp: "unknown"}
}

func parseLinuxProbeStatus(body []byte) linuxProbeStatus {
	result := unknownLinuxProbeStatus()
	seen := map[string]bool{}
	values := map[string]string{}
	duplicates := map[string]bool{}
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "Uid", "CapEff", "CapPrm", "CapBnd", "NoNewPrivs", "Seccomp":
		default:
			continue
		}
		if seen[key] {
			duplicates[key] = true
			continue
		}
		seen[key] = true
		values[key] = strings.TrimSpace(value)
	}
	if value, ok := values["Uid"]; ok && !duplicates["Uid"] {
		parts := strings.Fields(value)
		if len(parts) == 4 {
			ids := make([]uint64, len(parts))
			valid := true
			for i, part := range parts {
				id, err := strconv.ParseUint(part, 10, 32)
				if err != nil || strconv.FormatUint(id, 10) != part {
					valid = false
					break
				}
				ids[i] = id
			}
			if valid {
				euid := ids[1]
				if euid == 0 {
					result.euid = "root"
				} else {
					result.euid = "nonroot"
				}
			}
		}
	}
	result.capEffective = parseLinuxProbeCapability(values["CapEff"], seen["CapEff"] && !duplicates["CapEff"])
	result.capPermitted = parseLinuxProbeCapability(values["CapPrm"], seen["CapPrm"] && !duplicates["CapPrm"])
	result.capBounding = parseLinuxProbeCapability(values["CapBnd"], seen["CapBnd"] && !duplicates["CapBnd"])
	if value := values["NoNewPrivs"]; seen["NoNewPrivs"] && !duplicates["NoNewPrivs"] && (value == "0" || value == "1") {
		result.noNewPrivs = value
	}
	if value := values["Seccomp"]; seen["Seccomp"] && !duplicates["Seccomp"] && (value == "0" || value == "1" || value == "2") {
		result.seccomp = value
	}
	return result
}

func parseLinuxProbeCapability(value string, present bool) string {
	if !present || len(value) == 0 || len(value) > 16 {
		return "unknown"
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "unknown"
		}
	}
	bits, err := strconv.ParseUint(value, 16, 64)
	if err != nil {
		return "unknown"
	}
	if bits&(uint64(1)<<7) != 0 {
		return "yes"
	}
	return "no"
}

func parseLinuxProbeUIDMap(body []byte) string {
	if len(body) == 0 || len(body) > 1024 {
		return "unknown"
	}
	lines := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(lines) != 1 {
		return "unknown"
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 3 {
		return "unknown"
	}
	values := make([]uint64, len(fields))
	for i, field := range fields {
		if field == "" {
			return "unknown"
		}
		for _, c := range field {
			if c < '0' || c > '9' {
				return "unknown"
			}
		}
		value, err := strconv.ParseUint(field, 10, 32)
		if err != nil || strconv.FormatUint(value, 10) != field {
			return "unknown"
		}
		values[i] = value
	}
	if values[2] == 0 || values[0]+values[2] > uint64(1)<<32 || values[1]+values[2] > uint64(1)<<32 {
		return "unknown"
	}
	if values[0] == 0 && values[1] == 0 && values[2] == 4294967295 {
		return "initial"
	}
	return "other"
}

func (c linuxProbeContext) markers() []string {
	result := make([]string, 0, 13)
	appendStatus := func(source string, status linuxProbeStatus) {
		result = append(result,
			"native-update-schema-context:"+source+":euid:"+status.euid,
			"native-update-schema-context:"+source+":cap_setuid_effective:"+status.capEffective,
			"native-update-schema-context:"+source+":cap_setuid_permitted:"+status.capPermitted,
			"native-update-schema-context:"+source+":cap_setuid_bounding:"+status.capBounding,
			"native-update-schema-context:"+source+":no_new_privs:"+status.noNewPrivs,
			"native-update-schema-context:"+source+":seccomp:"+status.seccomp)
	}
	appendStatus("process", c.process)
	appendStatus("thread", c.thread)
	result = append(result, "native-update-schema-context:uid_map:"+c.uidMap)
	return result
}

func validLinuxProbeContextMarker(line string) bool {
	fields := strings.Split(line, ":")
	if len(fields) < 1 || fields[0] != "native-update-schema-context" {
		return false
	}
	if len(fields) == 3 && fields[1] == "uid_map" {
		return fields[2] == "initial" || fields[2] == "other" || fields[2] == "unknown"
	}
	if len(fields) != 4 || (fields[1] != "process" && fields[1] != "thread") {
		return false
	}
	switch fields[2] {
	case "euid":
		return fields[3] == "root" || fields[3] == "nonroot" || fields[3] == "unknown"
	case "cap_setuid_effective", "cap_setuid_permitted", "cap_setuid_bounding":
		return fields[3] == "yes" || fields[3] == "no" || fields[3] == "unknown"
	case "no_new_privs":
		return fields[3] == "0" || fields[3] == "1" || fields[3] == "unknown"
	case "seccomp":
		return fields[3] == "0" || fields[3] == "1" || fields[3] == "2" || fields[3] == "unknown"
	default:
		return false
	}
}
