//go:build windows

package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsAvailabilityDiagnosticErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"trust", windowsAvailabilityError(windowsAvailabilityTrust, errors.New("private-trust")), "Native updater unavailable (Windows code=0x4e010000)."},
		{"pipe_open_preserves_last_native_error", windowsAvailabilityError(windowsAvailabilityPipeOpen, errors.Join(context.DeadlineExceeded, windows.ERROR_ACCESS_DENIED)), "Native updater unavailable (Windows code=0x4e020005)."},
		{"process_open", windowsAvailabilityError(windowsAvailabilityProcessOpen, fmt.Errorf("private-pid: %w", windows.ERROR_ACCESS_DENIED)), "Native updater unavailable (Windows code=0x4e080005)."},
		{"path_mismatch", windowsAvailabilityError(windowsAvailabilityImagePath, nil), "Native updater unavailable (Windows code=0x4e0a0000)."},
		{"read_wait", windowsAvailabilityError(windowsAvailabilityPipeReadWait, errors.Join(context.DeadlineExceeded, windows.ERROR_NO_DATA)), "Native updater unavailable (Windows code=0x4e1300e8)."},
		{"invalid_stage_not_echoed", windowsAvailabilityError(windowsAvailabilityStage(255), windows.ERROR_ACCESS_DENIED), "Native updater unavailable (Windows code=0x4e1c0005)."},
		{"large_native_status_not_truncated", windowsAvailabilityError(windowsAvailabilityPipeRead, windows.Errno(0xc0000005)), "Native updater unavailable (Windows code=0x4e120000)."},
		{"unclassified_private_error", errors.New("private-arbitrary-error-body"), "Native updater unavailable (Windows code=0x4e1c0000)."},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Even a ready-looking response cannot override an identity/I/O error.
			available, reason := windowsAvailabilityResult("ready", fmt.Errorf("private-wrapper: %w", tt.err))
			if available || reason != tt.want || strings.Contains(reason, "private") {
				t.Fatalf("availability = %v, reason = %q; want false, %q", available, reason, tt.want)
			}
		})
	}
	err := windowsAvailabilityError(windowsAvailabilityProcessOpen, windows.ERROR_ACCESS_DENIED)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("diagnostic lost native cause")
	}
}

func TestWindowsAvailabilityReplyWhitelist(t *testing.T) {
	for _, tt := range []struct {
		reply     string
		available bool
		reason    string
	}{
		{"ready", true, ""},
		{"refused", false, "Native updater unavailable (Windows code=0x4e140000)."},
		{"disabled", false, "Native updater unavailable (Windows code=0x4e1a0000)."},
		{"busy", false, "Native updater unavailable (Windows code=0x4e1b0000)."},
		{"unavailable:4e160005", false, "Native updater unavailable (Windows code=0x4e160005)."},
		{"unavailable:4e170005", false, "Native updater unavailable (Windows code=0x4e170005)."},
		{"unavailable:4e180002", false, "Native updater unavailable (Windows code=0x4e180002)."},
		{"unavailable:4e190000", false, "Native updater unavailable (Windows code=0x4e190000)."},
	} {
		t.Run(tt.reply, func(t *testing.T) {
			available, reason := windowsAvailabilityResult(tt.reply, nil)
			if available != tt.available || reason != tt.reason {
				t.Fatalf("availability = %v, reason = %q", available, reason)
			}
		})
	}
	for _, reply := range []string{
		"accepted", "private-arbitrary-payload", "unavailable:4d160005", "unavailable:4e080005",
		"unavailable:4e1c0005", "unavailable:4e1d0005", "unavailable:4e000005", "unavailable:4e160005-extra",
		"unavailable:4E160005", "unavailable:04e160005", "unavailable:4e16zzzz", strings.Repeat("private", 1000),
	} {
		available, reason := windowsAvailabilityResult(reply, nil)
		if available || reason != "Native updater unavailable (Windows code=0x4e150000)." {
			t.Fatal("unrecognized payload escaped fixed reply classification")
		}
	}
}

func TestWindowsStatusFailureContainsOnlyAuthoredFields(t *testing.T) {
	reply := windowsStatusFailure(windowsAvailabilityServerTrust, fmt.Errorf("private-file: %w", windows.ERROR_ACCESS_DENIED))
	if reply != "unavailable:4e160005" {
		t.Fatal("status diagnostic differs from fixed wire format")
	}
	parsed, ok := parseWindowsStatusFailure(reply)
	if !ok || !errors.Is(parsed, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("status diagnostic lost bounded native code")
	}
}

func TestWindowsAvailabilityCanonicalAndCancellation(t *testing.T) {
	b := &nativeBroker{matches: false}
	if available, reason := b.AvailabilityStatus(context.Background()); available || reason != b.UnavailableReason() || reason == "" {
		t.Fatal("noncanonical installation passed detailed availability")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.matches = true
	if available, reason := b.AvailabilityStatus(ctx); available || reason != "Native updater unavailable (Windows code=0x4e010000)." {
		t.Fatal("canceled probe passed or leaked a cause")
	}
}

func TestWindowsAvailabilityChecksActualProcessImage(t *testing.T) {
	image, err := os.Executable()
	if err != nil {
		t.Fatal("locate test process image:", err)
	}
	if err := verifiedWindowsProcessImage(windows.CurrentProcess(), image); err != nil {
		t.Fatal("actual matching process image rejected:", err)
	}
	if err := verifiedWindowsProcessImage(windows.CurrentProcess(), ""); err == nil || err.Error() != "Native updater unavailable (Windows code=0x4e0a0000)." {
		t.Fatal("different expected image did not fail at fixed path-identity stage")
	}
	// -1 is a valid current-process pseudo-handle; NULL is invalid here.
	if err := verifiedWindowsProcessImage(windows.Handle(0), image); err == nil || err.Error() != "Native updater unavailable (Windows code=0x4e090006)." {
		t.Fatal("invalid process handle did not retain query stage/native error")
	}
}

func TestWindowsAvailabilityChecksActualPipeHandle(t *testing.T) {
	err := verifiedWindowsUpdaterPipe(windows.InvalidHandle)
	if err == nil || err.Error() != "Native updater unavailable (Windows code=0x4e030006)." || !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatal("invalid pipe handle lost verified-server-PID failure")
	}
}
