//go:build linux

package launcher

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func processIdentity(pid int) (start, exe, cwd string, err error) {
	return processIdentityUsing(pid, os.ReadFile, os.Readlink)
}

func processIdentityUsing(pid int, readFile func(string) ([]byte, error), readlink func(string) (string, error)) (start, exe, cwd string, err error) {
	if pid <= 0 {
		return "", "", "", fmt.Errorf("invalid PID")
	}
	boot, e := readFile("/proc/sys/kernel/random/boot_id")
	if e != nil {
		return "", "", "", processIdentityStageFailure(processIdentityBootIdentity, fmt.Errorf("boot identity: %w", e))
	}
	bootID := strings.TrimSpace(string(boot))
	if len(bootID) != 36 {
		return "", "", "", processIdentityStageFailure(processIdentityBootIdentity, fmt.Errorf("missing boot identity"))
	}
	base := "/proc/" + strconv.Itoa(pid)
	stat, e := readFile(base + "/stat")
	if e != nil {
		return "", "", "", linuxProcessIdentityFailure(processIdentityProcStat, e)
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return "", "", "", processIdentityStageFailure(processIdentityProcStat, fmt.Errorf("invalid proc stat"))
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return "", "", "", os.ErrNotExist
	}
	if _, e = strconv.ParseUint(fields[19], 10, 64); e != nil {
		return "", "", "", processIdentityStageFailure(processIdentityProcStat, e)
	}
	start = bootID + ":" + fields[19]
	exe, e = readlink(base + "/exe")
	if e != nil {
		return start, "", "", linuxProcessIdentityFailure(processIdentityProcExecutable, e)
	}
	cwd, e = readlink(base + "/cwd")
	if e != nil {
		return start, "", "", linuxProcessIdentityFailure(processIdentityProcWorkingDirectory, e)
	}
	return bootID + ":" + fields[19], filepath.Clean(exe), filepath.Clean(cwd), nil
}
func linuxProcessIdentityFailure(stage processIdentityStage, err error) error {
	if IsMissingProcess(err) {
		return err
	}
	return processIdentityStageFailure(stage, err)
}
func lockState(path string) (func(), error) {
	f, err := openPrivate(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("instance launcher already active: %w", err)
	}
	return func() { f.Close() }, nil
}
func replaceFile(from, to string) error    { return os.Rename(from, to) }
func confineChild(pid int) (func(), error) { return func() {}, nil }
func sameWorkingDirectory(actual string, id domain.ProcessIdentity) bool {
	return actual == filepath.Clean(id.WorkingDir)
}
func prepareChild(cmd *exec.Cmd)      {}
func configureStdin(f *os.File) error { return nil }
func writeStdin(f *os.File, p []byte) (int, error) {
	if err := f.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return 0, err
	}
	return f.Write(p)
}
func IsMissingProcess(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ESRCH)
}

func openPrivate(path string, flags int, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, perm)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("private state file must be regular")
	}
	return f, nil
}
