//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"golang.org/x/sys/windows"
)

const processIdentityRaceChildEnv = "MINECRAFT_UI_PROCESS_IDENTITY_RACE_CHILD"

// This child is started only by the deterministic handle-exit race test.
func TestWindowsProcessIdentityRaceChild(t *testing.T) {
	if os.Getenv(processIdentityRaceChildEnv) != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func startProcessIdentityRaceChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessIdentityRaceChild$")
	cmd.Env = append(os.Environ(), processIdentityRaceChildEnv+"=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState != nil {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// QueryFullProcessImageName can retain the 8.3 spelling used to start Go's test
// binary while CaptureIdentity normalizes the LaunchSpec through EvalSymlinks.
func TestWindowsProcessImageAliasesRequireSameCanonicalFilesystemPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	start, got, _, err := processIdentity(os.Getpid())
	if err != nil || start == "" || !equalExecutable(got, want) {
		t.Fatalf("process image=%q want=%q start=%q error=%v", got, want, start, err)
	}
	name, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(name, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("short image path: length=%d error=%v", n, err)
	}
	short := windows.UTF16ToString(buffer[:n])
	if short == want {
		t.Log("volume did not provide a distinct 8.3 alias; canonical native image check still exercised")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id, err := CaptureIdentity("image", os.Getpid(), domain.LaunchSpec{Executable: short, WorkingDir: cwd})
	if err != nil || id.Executable != want {
		t.Fatalf("capture using short alias: identity=%+v error=%v", id, err)
	}
	if err := VerifyIdentity(id); err != nil {
		t.Fatal(err)
	}
	// An existing different file must never become an acceptable image identity.
	other := filepath.Join(t.TempDir(), "other.exe")
	if err := os.WriteFile(other, []byte("different executable"), 0600); err != nil {
		t.Fatal(err)
	}
	id.Executable = other
	if err := VerifyIdentity(id); err == nil {
		t.Fatal("different image accepted for the same live PID")
	}
	id.Executable = filepath.Join(t.TempDir(), "missing.exe")
	if alive, err := ProbeIdentity(id); alive || err == nil {
		t.Fatalf("unavailable image binding classified as process exit: alive=%v error=%v", alive, err)
	}
}

func TestWindowsProcessIdentityRechecksHeldHandleAfterMetadataFailure(t *testing.T) {
	cmd := startProcessIdentityRaceChild(t)
	metadataErr := fmt.Errorf("injected process metadata failure")
	ops := nativeWindowsProcessIdentityOps()
	childWaited := false
	ops.afterWait = func(windows.Handle) {
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("terminate harmless child after initial wait: %v", err)
			return
		}
		_ = cmd.Wait()
		childWaited = true
	}
	ops.getTimes = func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
		return metadataErr
	}
	_, _, _, err := processIdentityWithOps(cmd.Process.Pid, ops)
	if !childWaited {
		t.Fatal("test child was not stopped after initial WAIT_TIMEOUT")
	}
	if !errors.Is(err, os.ErrNotExist) || !IsMissingProcess(err) {
		t.Fatalf("signaled held handle did not prove exit: error=%v", err)
	}
	// Before the recheck, the exact injected metadata error would have been
	// returned. The recheck is what turns this race into a conclusive exit.
	if errors.Is(metadataErr, os.ErrNotExist) {
		t.Fatal("negative-control metadata error unexpectedly represented exit")
	}
}

func TestWindowsProcessIdentityRechecksHeldHandleAfterImageFailure(t *testing.T) {
	cmd := startProcessIdentityRaceChild(t)
	metadataErr := fmt.Errorf("injected process image failure")
	ops := nativeWindowsProcessIdentityOps()
	childWaited := false
	queryReached := false
	ops.afterWait = func(windows.Handle) {
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("terminate harmless child after initial wait: %v", err)
			return
		}
		_ = cmd.Wait()
		childWaited = true
	}
	// Leave GetProcessTimes native so this case isolates the image query branch.
	ops.queryImage = func(windows.Handle, uint32, *uint16, *uint32) error {
		queryReached = true
		return metadataErr
	}
	_, _, _, err := processIdentityWithOps(cmd.Process.Pid, ops)
	if !childWaited || !queryReached {
		t.Fatalf("native process times did not reach injected image failure: child_waited=%v query_reached=%v", childWaited, queryReached)
	}
	if !errors.Is(err, os.ErrNotExist) || !IsMissingProcess(err) {
		t.Fatalf("signaled held handle did not prove exit after image failure: error=%v", err)
	}
}

func TestWindowsProcessIdentityNativeMetadataDuringExitRaceIsConclusive(t *testing.T) {
	cmd := startProcessIdentityRaceChild(t)
	ops := nativeWindowsProcessIdentityOps()
	childWaited := false
	ops.afterWait = func(windows.Handle) {
		if err := cmd.Process.Kill(); err != nil {
			t.Errorf("terminate harmless child after initial wait: %v", err)
			return
		}
		_ = cmd.Wait()
		childWaited = true
	}
	start, image, _, err := processIdentityWithOps(cmd.Process.Pid, ops)
	if !childWaited {
		t.Fatal("test child was not stopped after initial WAIT_TIMEOUT")
	}
	if err == nil {
		if start == "" || image == "" {
			t.Fatalf("native metadata returned incomplete identity: start=%q image=%q", start, image)
		}
		return
	}
	if !IsMissingProcess(err) {
		t.Fatalf("signaled native process handle produced an ambiguous error: %v", err)
	}
}

func TestWindowsProcessIdentityMetadataErrorsRequireConclusiveRecheck(t *testing.T) {
	t.Run("live process preserves metadata error", func(t *testing.T) {
		cmd := startProcessIdentityRaceChild(t)
		metadataErr := windows.ERROR_INVALID_PARAMETER
		ops := nativeWindowsProcessIdentityOps()
		ops.getTimes = func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
			return metadataErr
		}
		_, _, _, err := processIdentityWithOps(cmd.Process.Pid, ops)
		if !errors.Is(err, metadataErr) || IsMissingProcess(err) {
			t.Fatalf("live metadata error was recast as exit: error=%v", err)
		}
	})
	t.Run("live process preserves image error", func(t *testing.T) {
		cmd := startProcessIdentityRaceChild(t)
		metadataErr := windows.ERROR_INVALID_PARAMETER
		ops := nativeWindowsProcessIdentityOps()
		ops.queryImage = func(windows.Handle, uint32, *uint16, *uint32) error {
			return metadataErr
		}
		_, _, _, err := processIdentityWithOps(cmd.Process.Pid, ops)
		if !errors.Is(err, metadataErr) || IsMissingProcess(err) {
			t.Fatalf("live image error was recast as exit: error=%v", err)
		}
	})

	for _, tc := range []struct {
		name    string
		state   uint32
		waitErr error
	}{
		{name: "wait abandoned", state: uint32(windows.WAIT_ABANDONED)},
		{name: "wait timeout", state: uint32(windows.WAIT_TIMEOUT)},
		{name: "unknown wait result", state: 0xdead},
		{name: "wait failure", state: uint32(windows.WAIT_OBJECT_0), waitErr: fmt.Errorf("injected wait failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, metadataErr := range []error{fmt.Errorf("injected metadata error"), windows.ERROR_INVALID_PARAMETER, os.ErrNotExist} {
				calls := 0
				ops := windowsProcessIdentityOps{
					openProcess: func(uint32, bool, uint32) (windows.Handle, error) { return windows.Handle(1), nil },
					closeHandle: func(windows.Handle) error { return nil },
					wait: func(windows.Handle, uint32) (uint32, error) {
						calls++
						if calls == 1 {
							return uint32(windows.WAIT_TIMEOUT), nil
						}
						return tc.state, tc.waitErr
					},
					getTimes: func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
						return metadataErr
					},
				}
				_, _, _, err := processIdentityWithOps(123, ops)
				if !errors.Is(err, metadataErr) || IsMissingProcess(err) {
					t.Fatalf("ambiguous recheck changed metadata error %v: error=%v", metadataErr, err)
				}
			}
		})
	}
}

func TestWindowsProcessIdentityFailureStagesIdentifyNativeAPIs(t *testing.T) {
	for _, stage := range []string{"open_process", "wait_process", "get_process_times", "query_image", "boot_identity"} {
		t.Run(stage, func(t *testing.T) {
			nativeErr := windows.ERROR_ACCESS_DENIED
			ops := windowsProcessIdentityOps{
				openProcess: func(uint32, bool, uint32) (windows.Handle, error) {
					return windows.Handle(17), nil
				},
				closeHandle: func(windows.Handle) error { return nil },
				wait: func(windows.Handle, uint32) (uint32, error) {
					return uint32(windows.WAIT_TIMEOUT), nil
				},
				getTimes: func(_ windows.Handle, creation, exit, kernel, user *windows.Filetime) error {
					creation.LowDateTime = 1
					return nil
				},
				queryImage: func(_ windows.Handle, _ uint32, buffer *uint16, size *uint32) error {
					*buffer = 'x'
					*size = 1
					return nil
				},
				bootID: func() (windows.GUID, error) { return windows.GUID{Data1: 1}, nil },
			}
			switch stage {
			case "open_process":
				ops.openProcess = func(uint32, bool, uint32) (windows.Handle, error) { return 0, nativeErr }
			case "wait_process":
				ops.wait = func(windows.Handle, uint32) (uint32, error) { return 0, nativeErr }
			case "get_process_times":
				ops.getTimes = func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error {
					return nativeErr
				}
			case "query_image":
				ops.queryImage = func(windows.Handle, uint32, *uint16, *uint32) error { return nativeErr }
			case "boot_identity":
				ops.bootID = func() (windows.GUID, error) { return windows.GUID{}, nativeErr }
			}

			_, _, _, err := processIdentityWithOps(17, ops)
			if err == nil || !errors.Is(err, nativeErr) || IsMissingProcess(err) {
				t.Fatalf("native error was not preserved as a live-process failure: %v", err)
			}
			if got := ProcessIdentityFailureStage(err); got != stage {
				t.Fatalf("native process identity stage = %q, want %q", got, stage)
			}
		})
	}
}

func TestWindowsProcessIdentityFailureStagesPreserveMissingClassification(t *testing.T) {
	t.Run("open process invalid parameter remains missing", func(t *testing.T) {
		ops := nativeWindowsProcessIdentityOps()
		ops.openProcess = func(uint32, bool, uint32) (windows.Handle, error) {
			return 0, windows.ERROR_INVALID_PARAMETER
		}
		_, _, _, err := processIdentityWithOps(17, ops)
		if !IsMissingProcess(err) || !errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			t.Fatalf("open process invalid-parameter classification changed: %v", err)
		}
	})

	for _, cause := range []error{windows.ERROR_INVALID_PARAMETER, os.ErrNotExist} {
		t.Run("boot identity does not prove exit: "+cause.Error(), func(t *testing.T) {
			ops := windowsProcessIdentityOps{
				openProcess: func(uint32, bool, uint32) (windows.Handle, error) { return windows.Handle(17), nil },
				closeHandle: func(windows.Handle) error { return nil },
				wait:        func(windows.Handle, uint32) (uint32, error) { return uint32(windows.WAIT_TIMEOUT), nil },
				getTimes: func(_ windows.Handle, creation, _, _, _ *windows.Filetime) error {
					creation.LowDateTime = 1
					return nil
				},
				queryImage: func(_ windows.Handle, _ uint32, buffer *uint16, size *uint32) error {
					*buffer = 'x'
					*size = 1
					return nil
				},
				bootID: func() (windows.GUID, error) { return windows.GUID{}, cause },
			}
			_, _, _, err := processIdentityWithOps(17, ops)
			if !errors.Is(err, cause) || IsMissingProcess(err) {
				t.Fatalf("boot identity error was misclassified as process exit: %v", err)
			}
			if ProcessIdentityFailureStage(err) != "boot_identity" {
				t.Fatalf("boot identity stage was lost: %q", ProcessIdentityFailureStage(err))
			}
		})
	}
}

func TestWindowsProcessIdentityInitialWaitRequiresKnownResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   uint32
		waitErr error
		missing bool
	}{
		{name: "signaled", state: uint32(windows.WAIT_OBJECT_0), missing: true},
		{name: "abandoned", state: uint32(windows.WAIT_ABANDONED)},
		{name: "unknown", state: 0xbeef},
		{name: "wait error resembling missing PID", state: uint32(windows.WAIT_TIMEOUT), waitErr: windows.ERROR_INVALID_PARAMETER},
		{name: "wait not-exist error", state: uint32(windows.WAIT_TIMEOUT), waitErr: os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := windowsProcessIdentityOps{
				openProcess: func(uint32, bool, uint32) (windows.Handle, error) { return windows.Handle(1), nil },
				closeHandle: func(windows.Handle) error { return nil },
				wait:        func(windows.Handle, uint32) (uint32, error) { return tc.state, tc.waitErr },
			}
			_, _, _, err := processIdentityWithOps(123, ops)
			if IsMissingProcess(err) != tc.missing {
				t.Fatalf("initial wait classification=%v want=%v error=%v", IsMissingProcess(err), tc.missing, err)
			}
			if tc.waitErr != nil && !errors.Is(err, tc.waitErr) {
				t.Fatalf("initial wait error not preserved: got=%v want=%v", err, tc.waitErr)
			}
		})
	}
}
