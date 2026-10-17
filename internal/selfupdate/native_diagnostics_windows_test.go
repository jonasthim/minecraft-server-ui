//go:build windows

package selfupdate

import (
	"errors"
	"fmt"
	"syscall"
	"testing"

	"golang.org/x/sys/windows/svc"
)

type testWindowsDiagnosticWriter struct {
	events []struct {
		id      uint32
		message string
	}
	infoErr  error
	closeErr error
}

func (w *testWindowsDiagnosticWriter) Info(id uint32, message string) error {
	w.events = append(w.events, struct {
		id      uint32
		message string
	}{id, message})
	return w.infoErr
}
func (w *testWindowsDiagnosticWriter) Close() error { return w.closeErr }

func TestWindowsActivationDiagnosticAcceptsOnlyFixedStages(t *testing.T) {
	for _, stage := range []string{"schema_probe", "candidate_probe", "manager_stop"} {
		message, ok := windowsActivationDiagnosticMessage(stage)
		if !ok || message != "activation_stage="+stage {
			t.Fatalf("fixed stage %q: message %q, accepted %t", stage, message, ok)
		}
	}
	for _, stage := range []string{"schema_probe;path=C:\\private", "private-path", "unknown"} {
		if message, ok := windowsActivationDiagnosticMessage(stage); ok || message != "" {
			t.Fatalf("unbounded stage accepted: %q", message)
		}
	}
}

func TestWindowsProbeDiagnosticContainsOnlyFixedStepAndBoundedCode(t *testing.T) {
	message, ok := windowsProbeDiagnosticMessage("service_start", fmt.Errorf("private path C:\\secret: %w", syscall.Errno(5)))
	if !ok || message != "probe_step=service_start;win32_code=5" {
		t.Fatalf("numeric diagnostic = %q, accepted %t", message, ok)
	}
	message, ok = windowsProbeDiagnosticMessage("result_read", errors.New("private path C:\\secret credential token"))
	if !ok || message != "probe_step=result_read;win32_code=unknown" {
		t.Fatalf("unknown diagnostic = %q, accepted %t", message, ok)
	}
	if message, ok = windowsProbeDiagnosticMessage("service_start;path=C:\\secret", syscall.Errno(5)); ok || message != "" {
		t.Fatalf("unbounded probe step accepted: %q", message)
	}
	if message, ok = windowsProbeDiagnosticMessage("service_start", syscall.Errno(1<<40)); !ok || message != "probe_step=service_start;win32_code=unknown" {
		t.Fatalf("overflow errno was not reduced to unknown: %q, accepted %t", message, ok)
	}
}

func TestWindowsProbeSCMDiagnosticRequiresStoppedAndUsesOnlyNumericFields(t *testing.T) {
	message, ok := windowsProbeSCMDiagnosticMessage(svc.Status{State: svc.Stopped, Win32ExitCode: 5, ServiceSpecificExitCode: 0x4d000000})
	if !ok || message != "probe_state=stopped;win32_exit_code=5;service_specific_exit_code=1291845632" {
		t.Fatalf("SCM diagnostic = %q, accepted %t", message, ok)
	}
	if message, ok := windowsProbeSCMDiagnosticMessage(svc.Status{State: svc.Running}); ok || message != "" {
		t.Fatalf("running service produced stopped diagnostic: %q", message)
	}
}

func TestWindowsDiagnosticReporterFailureIsBestEffort(t *testing.T) {
	writer := &testWindowsDiagnosticWriter{infoErr: errors.New("private reporter failure"), closeErr: errors.New("private close failure")}
	old := openWindowsDiagnosticEventLog
	openWindowsDiagnosticEventLog = func() (windowsDiagnosticEventWriter, error) { return writer, nil }
	t.Cleanup(func() { openWindowsDiagnosticEventLog = old })

	(windowsDriver{}).ActivationFailure("schema_probe")
	if len(writer.events) != 1 || writer.events[0].id != windowsEventActivationFailure || writer.events[0].message != "activation_stage=schema_probe" {
		t.Fatalf("event was not attempted with fixed payload: %#v", writer.events)
	}
	openWindowsDiagnosticEventLog = func() (windowsDiagnosticEventWriter, error) { return nil, errors.New("private open failure") }
	writeWindowsDiagnosticEvent(windowsEventProbeFailure, "probe_step=service_start;win32_code=unknown")
	(windowsDriver{}).ActivationFailure("schema_probe;private-path")
	if len(writer.events) != 1 {
		t.Fatalf("invalid stage was reported: %#v", writer.events)
	}
}
