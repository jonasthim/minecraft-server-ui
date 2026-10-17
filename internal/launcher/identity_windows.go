//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"golang.org/x/sys/windows"
)

type windowsProcessIdentityOps struct {
	openProcess func(uint32, bool, uint32) (windows.Handle, error)
	closeHandle func(windows.Handle) error
	wait        func(windows.Handle, uint32) (uint32, error)
	getTimes    func(windows.Handle, *windows.Filetime, *windows.Filetime, *windows.Filetime, *windows.Filetime) error
	queryImage  func(windows.Handle, uint32, *uint16, *uint32) error
	bootID      func() (windows.GUID, error)
	afterWait   func(windows.Handle)
}

const processMetadataExitWaitMilliseconds uint32 = 1000

func nativeWindowsProcessIdentityOps() windowsProcessIdentityOps {
	return windowsProcessIdentityOps{
		openProcess: windows.OpenProcess,
		closeHandle: windows.CloseHandle,
		wait:        windows.WaitForSingleObject,
		getTimes:    windows.GetProcessTimes,
		queryImage:  windows.QueryFullProcessImageName,
		bootID:      windowsBootIdentifier,
	}
}

func processIdentity(pid int) (start, exe, cwd string, err error) {
	return processIdentityWithOps(pid, nativeWindowsProcessIdentityOps())
}

func processIdentityWithOps(pid int, ops windowsProcessIdentityOps) (start, exe, cwd string, err error) {
	if pid <= 0 {
		return "", "", "", fmt.Errorf("invalid PID")
	}
	h, e := ops.openProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if e != nil {
		return "", "", "", processIdentityStageFailure(processIdentityOpenProcess, e)
	}
	defer ops.closeHandle(h)
	state, e := ops.wait(h, 0)
	if e != nil {
		return "", "", "", &processWaitUnavailable{cause: processIdentityStageFailure(processIdentityWaitProcess, e)}
	}
	if state == uint32(windows.WAIT_OBJECT_0) {
		return "", "", "", os.ErrNotExist
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return "", "", "", processIdentityStageFailure(processIdentityWaitProcess, fmt.Errorf("unexpected process wait state 0x%08x", state))
	}
	if ops.afterWait != nil {
		ops.afterWait(h)
	}
	var creation, exit, kernel, user windows.Filetime
	if e = ops.getTimes(h, &creation, &exit, &kernel, &user); e != nil {
		return "", "", "", processMetadataError(h, processIdentityStageFailure(processIdentityGetProcessTimes, e), ops.wait)
	}
	var buffer [32768]uint16
	n := uint32(len(buffer))
	if e = ops.queryImage(h, 0, &buffer[0], &n); e != nil {
		return "", "", "", processMetadataError(h, processIdentityStageFailure(processIdentityQueryImage, e), ops.wait)
	}
	bootID, e := ops.bootID()
	if e != nil {
		return "", "", "", processIdentityStageFailure(processIdentityBootIdentity, fmt.Errorf("boot identity: %w", e))
	}
	if bootID == (windows.GUID{}) {
		return "", "", "", processIdentityStageFailure(processIdentityBootIdentity, fmt.Errorf("missing boot identity"))
	}
	return fmt.Sprintf("%s:%08x%08x", bootID.String(), creation.HighDateTime, creation.LowDateTime), windows.UTF16ToString(buffer[:n]), "", nil
}

// A process may exit after the initial zero-time wait and before a metadata
// query completes. After a metadata failure, wait briefly on the same held
// process handle so an exit racing the query can become observable. Only a
// signaled result proves exit; every failed or ambiguous wait preserves the
// metadata error.
func processMetadataError(handle windows.Handle, metadataErr error, wait func(windows.Handle, uint32) (uint32, error)) error {
	state, waitErr := wait(handle, processMetadataExitWaitMilliseconds)
	if waitErr == nil && state == uint32(windows.WAIT_OBJECT_0) {
		return os.ErrNotExist
	}
	cause := metadataErr
	if waitErr != nil {
		cause = errors.Join(cause, processIdentityStageFailure(processIdentityWaitProcess, waitErr))
	} else if state != uint32(windows.WAIT_TIMEOUT) {
		cause = errors.Join(cause, processIdentityStageFailure(processIdentityWaitProcess, fmt.Errorf("unexpected process wait state 0x%08x", state)))
	}
	return &processMetadataUnavailable{cause: cause}
}

type processMetadataUnavailable struct {
	cause error
}

func (e *processMetadataUnavailable) Error() string { return e.cause.Error() }
func (e *processMetadataUnavailable) Unwrap() error { return e.cause }

type processWaitUnavailable struct {
	cause error
}

func (e *processWaitUnavailable) Error() string { return e.cause.Error() }
func (e *processWaitUnavailable) Unwrap() error { return e.cause }

func windowsBootIdentifier() (windows.GUID, error) {
	// BootIdentifier is a kernel-generated GUID. Fail closed if this system
	// information class is unavailable; never approximate boot time from a clock.
	var boot struct {
		Identifier   windows.GUID
		FirmwareType uint32
		BootFlags    uint64
	}
	var size uint32
	if err := windows.NtQuerySystemInformation(windows.SystemBootEnvironmentInformation, unsafe.Pointer(&boot), uint32(unsafe.Sizeof(boot)), &size); err != nil {
		return windows.GUID{}, err
	}
	return boot.Identifier, nil
}

// Windows does not expose remote cwd through a stable documented Win32 API.
// The private launcher identity and authenticated control bind the launch cwd;
// OS verification independently binds boot GUID, process creation and image.
func sameWorkingDirectory(_ string, id domain.ProcessIdentity) bool {
	return filepath.IsAbs(id.WorkingDir)
}
func equalExecutable(a, b string) bool {
	// The OS image name can retain an 8.3 alias (e.g. RUNNER~1). Resolve
	// both existing bindings through the filesystem before comparing complete
	// paths. Failure here is a live metadata mismatch, never proof of PID exit.
	canonicalA, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	canonicalB, err := filepath.EvalSymlinks(b)
	return err == nil && strings.EqualFold(canonicalA, canonicalB)
}
func lockState(path string) (func(), error) {
	f, err := openPrivate(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
func replaceFile(from, to string) error {
	a, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	b, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(a, b, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW}
}
func confineChild(pid int) (func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() { windows.CloseHandle(job) }
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		closeJob()
		return nil, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(proc)
	if err = windows.AssignProcessToJobObject(job, proc); err != nil {
		closeJob()
		return nil, err
	}
	// os/exec closes CreateProcess's primary-thread handle. Enumerate the suspended
	// child's sole thread after containment, then resume it. No JVM code runs
	// before successful job assignment; any failure leaves it suspended for kill.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		closeJob()
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	for err = windows.Thread32First(snapshot, &entry); err == nil; err = windows.Thread32Next(snapshot, &entry) {
		if entry.OwnerProcessID != uint32(pid) {
			continue
		}
		thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
		if e != nil {
			closeJob()
			return nil, e
		}
		_, e = windows.ResumeThread(thread)
		windows.CloseHandle(thread)
		if e != nil {
			closeJob()
			return nil, e
		}
		return closeJob, nil
	}
	closeJob()
	return nil, fmt.Errorf("suspended JVM thread not found: %w", err)
}
func configureStdin(f *os.File) error {
	mode := uint32(windows.PIPE_NOWAIT)
	return windows.SetNamedPipeHandleState(windows.Handle(f.Fd()), &mode, nil, nil)
}
func writeStdin(f *os.File, p []byte) (int, error) { return f.Write(p) }
func IsMissingProcess(err error) bool {
	var metadataErr *processMetadataUnavailable
	if errors.As(err, &metadataErr) {
		return false
	}
	var waitErr *processWaitUnavailable
	if errors.As(err, &waitErr) {
		return false
	}
	var stageErr *processIdentityStageError
	if errors.As(err, &stageErr) && stageErr != nil && stageErr.stage == processIdentityOpenProcess {
		err = stageErr.cause
	}
	return os.IsNotExist(err) || err == windows.ERROR_INVALID_PARAMETER
}

func openPrivate(path string, flags int, perm os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flags&os.O_RDWR != 0 {
		access |= windows.GENERIC_WRITE
	} else if flags&os.O_WRONLY != 0 {
		access = windows.GENERIC_WRITE
	}
	if flags&os.O_APPEND != 0 {
		access &^= windows.GENERIC_WRITE
		access |= windows.FILE_APPEND_DATA
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		disposition = windows.OPEN_ALWAYS
	}
	if flags&os.O_TRUNC != 0 {
		disposition = windows.CREATE_ALWAYS
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("private state file is not a regular file")
	}
	return os.NewFile(uintptr(h), path), nil
}
