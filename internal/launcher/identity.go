package launcher

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func CaptureIdentity(id string, pid int, s domain.LaunchSpec) (domain.ProcessIdentity, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return domain.ProcessIdentity{}, err
	}
	start, exe, cwd, err := processIdentity(pid)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	wanted, err := filepath.EvalSymlinks(s.Executable)
	if err != nil {
		return domain.ProcessIdentity{}, err
	}
	out := domain.ProcessIdentity{InstanceID: id, PID: pid, LauncherPID: os.Getpid(), StartIdentity: start, Executable: wanted, WorkingDir: s.WorkingDir, JavaRuntimeID: s.JavaRuntimeID, JavaChecksum: s.JavaChecksum, StartedAt: time.Now().UTC()}
	if !equalExecutable(exe, wanted) || !sameWorkingDirectory(cwd, out) {
		return domain.ProcessIdentity{}, fmt.Errorf("child executable or working directory mismatch")
	}
	return out, nil
}
func VerifyIdentity(id domain.ProcessIdentity) error {
	if err := domain.ValidateInstanceID(id.InstanceID); err != nil {
		return err
	}
	if id.PID <= 0 || id.StartIdentity == "" || !filepath.IsAbs(id.Executable) || !filepath.IsAbs(id.WorkingDir) {
		return fmt.Errorf("incomplete process identity")
	}
	start, exe, cwd, err := processIdentity(id.PID)
	if err != nil {
		return fmt.Errorf("inspect process identity: %w", err)
	}
	if start != id.StartIdentity || !equalExecutable(exe, id.Executable) || !sameWorkingDirectory(cwd, id) {
		return fmt.Errorf("process identity mismatch; refusing reused PID")
	}
	return nil
}

// ProbeIdentity distinguishes a conclusively exited original process (including
// a different boot/start token) from unreadable or ambiguous process metadata.
// It never signals the PID, including when that PID now belongs to a stranger.
func ProbeIdentity(id domain.ProcessIdentity) (bool, error) {
	if err := domain.ValidateInstanceID(id.InstanceID); err != nil {
		return false, err
	}
	if id.PID <= 0 || id.StartIdentity == "" || !filepath.IsAbs(id.Executable) || !filepath.IsAbs(id.WorkingDir) {
		return false, fmt.Errorf("incomplete process identity")
	}
	start, exe, cwd, err := processIdentity(id.PID)
	if err != nil {
		if IsMissingProcess(err) {
			return false, nil
		}
		return false, err
	}
	if start != id.StartIdentity {
		return false, nil
	}
	if !equalExecutable(exe, id.Executable) || !sameWorkingDirectory(cwd, id) {
		return false, fmt.Errorf("live process metadata does not match")
	}
	return true, nil
}

// AcquireAdmission takes the same exclusive OS lock held for the complete
// launcher lifetime. Use only for short stopped-instance mutations.
func AcquireAdmission(dataDir, id string) (func(), error) {
	if _, err := Paths(dataDir, id); err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, "locks")
	if err := rejectSymlinks(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return lockState(filepath.Join(dir, id+".lock"))
}

// AcquireManager excludes another manager before database migrations or job
// recovery. The lock file is never removed; only closing the OS lock releases it.
func AcquireManager(dataDir string) (func(), error) {
	if !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("data directory must be absolute")
	}
	if err := rejectSymlinks(dataDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	release, err := lockState(filepath.Join(dataDir, "manager.lock"))
	if err != nil {
		return nil, fmt.Errorf("another manager owns this data directory: %w", err)
	}
	return release, nil
}
func rejectSymlinks(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in private state path")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(p) == p {
			return nil
		}
	}
}
