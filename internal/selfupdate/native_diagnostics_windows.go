//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const (
	windowsDiagnosticEventSource         = "MinecraftUIUpdater"
	windowsEventActivationFailure uint32 = 1001
	windowsEventProbeFailure      uint32 = 1002
	windowsEventProbeStopped      uint32 = 1003
)

type windowsDiagnosticEventWriter interface {
	Info(uint32, string) error
	Close() error
}

var openWindowsDiagnosticEventLog = func() (windowsDiagnosticEventWriter, error) {
	return eventlog.Open(windowsDiagnosticEventSource)
}

func writeWindowsDiagnosticEvent(id uint32, message string) {
	writer, err := openWindowsDiagnosticEventLog()
	if err != nil {
		return
	}
	_ = writer.Info(id, message)
	_ = writer.Close()
}

func windowsActivationDiagnosticMessage(stage string) (string, bool) {
	switch stage {
	case "candidate_fence", "journal_absence", "prior_hash", "prepared_journal", "challenge_publish", "manager_stop", "manager_lock", "drain_read", "drain_validate", "schema_probe", "schema_validate", "stopped_journal", "scratch_check", "previous_copy", "candidate_copy", "executable_fence", "candidate_probe", "candidate_validate", "candidate_hash":
		return "activation_stage=" + stage, true
	default:
		return "", false
	}
}

func (windowsDriver) ActivationFailure(stage string) {
	if message, ok := windowsActivationDiagnosticMessage(stage); ok {
		writeWindowsDiagnosticEvent(windowsEventActivationFailure, message)
	}
}

func windowsProbeDiagnosticMessage(step string, err error) (string, bool) {
	if err == nil {
		return "", false
	}
	switch step {
	case "bin_open", "probe_remove", "probe_copy", "bin_fence", "control_open", "control_remove", "challenge_read", "capability_encode", "capability_write", "service_open", "service_config", "service_binding", "account_sid", "configured_sid", "recovery_actions", "recovery_action_check", "service_start", "service_query", "service_timeout", "result_read", "result_validate":
	default:
		return "", false
	}
	code := "unknown"
	var nativeErrno syscall.Errno
	if errors.As(err, &nativeErrno) && nativeErrno > 0 && uint64(nativeErrno) <= uint64(^uint32(0)) {
		code = strconv.FormatUint(uint64(nativeErrno), 10)
	}
	return fmt.Sprintf("probe_step=%s;win32_code=%s", step, code), true
}

func windowsProbeSCMDiagnosticMessage(status svc.Status) (string, bool) {
	if status.State != svc.Stopped {
		return "", false
	}
	return fmt.Sprintf("probe_state=stopped;win32_exit_code=%d;service_specific_exit_code=%d", status.Win32ExitCode, status.ServiceSpecificExitCode), true
}

func logWindowsProbeFailure(step string, err error) {
	if message, ok := windowsProbeDiagnosticMessage(step, err); ok {
		writeWindowsDiagnosticEvent(windowsEventProbeFailure, message)
	}
}

func logWindowsProbeStopped(status svc.Status) {
	if message, ok := windowsProbeSCMDiagnosticMessage(status); ok {
		writeWindowsDiagnosticEvent(windowsEventProbeStopped, message)
	}
}
