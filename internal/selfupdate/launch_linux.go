//go:build linux

package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"golang.org/x/sys/unix"
)

// A launch permit only bridges the synchronous systemd dependency callback. It
// authorizes no caller-selected image and is valid only while its exact worker
// holds the kernel transaction lock. No persistent successful recovery cache.
type launchPermit struct {
	Format  int    `json:"format"`
	PID     int    `json:"pid"`
	Start   string `json:"start"`
	StageID string `json:"stage_id"`
	Nonce   string `json:"nonce"`
	Phase   string `json:"phase"`
	SHA256  string `json:"sha256"`
}

func launchImage(j activationJournal) (Installed, error) {
	switch j.Phase {
	case "candidate":
		return j.Candidate, nil
	case "rollback", "aborted":
		return j.Prior, nil
	default:
		return Installed{}, errors.New("journal phase does not permit manager launch")
	}
}
func procRead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("native process identity exceeds bound")
	}
	return b, nil
}
func processStart(pid int) (string, error) {
	if pid < 1 {
		return "", errors.New("invalid worker PID")
	}
	b, err := procRead(fmt.Sprintf("/proc/%d/stat", pid), 8192)
	if err != nil {
		return "", err
	}
	i := strings.LastIndexByte(string(b), ')')
	if i < 0 {
		return "", errors.New("invalid worker stat")
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return "", errors.New("incomplete worker stat")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", err
	}
	return fields[19], nil
}
func transactionLockOwner(state *os.Root) (int, error) {
	info, err := state.Lstat("manager.lock")
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return 0, errors.New("invalid native transaction lock")
	}
	b, err := procRead("/proc/locks", 1<<20)
	if err != nil {
		return 0, err
	}
	device := fmt.Sprintf("%02x:%02x:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)), stat.Ino)
	owner, matches := 0, 0
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 8 && f[1] == "FLOCK" && f[2] == "ADVISORY" && f[3] == "WRITE" && f[5] == device && f[6] == "0" && f[7] == "EOF" {
			owner, err = strconv.Atoi(f[4])
			if err != nil || owner < 1 {
				return 0, errors.New("ambiguous native transaction lock owner")
			}
			matches++
		}
	}
	if matches > 1 {
		return 0, errors.New("ambiguous native transaction locks")
	}
	return owner, nil
}
func ownsTransactionLock(state *os.Root, pid int) error {
	owner, err := transactionLockOwner(state)
	if err != nil {
		return err
	}
	if owner != pid {
		return errors.New("launch worker does not own the native transaction lock")
	}
	return nil
}
func validateLaunch(ctx context.Context, paths nativePaths, state, bin *os.Root, p launchPermit) error {
	if p.Format != 1 || p.PID < 1 || !validHash(p.StageID) || !validHash(p.Nonce) || !validHash(p.SHA256) {
		return errors.New("invalid native launch permit")
	}
	start, err := processStart(p.PID)
	if err != nil || start != p.Start {
		return errors.New("native launch worker no longer matches")
	}
	actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", p.PID))
	if err != nil {
		return err
	}
	expected, err := os.Stat(paths.Updater)
	if err != nil || !os.SameFile(actual, expected) {
		return errors.New("native launch worker executable mismatch")
	}
	if err := ownsTransactionLock(state, p.PID); err != nil {
		return err
	}
	worker := activationWorker{state: state}
	j, err := worker.load(ctx)
	if err != nil {
		return err
	}
	image, err := launchImage(j)
	if err != nil {
		return err
	}
	if j.Request.StageID != p.StageID || j.Nonce != p.Nonce || j.Phase != p.Phase || image.SHA256 != p.SHA256 {
		return errors.New("launch permit does not bind current journal")
	}
	h, err := hashFile(ctx, bin, paths.Binary)
	if err != nil || h != image.SHA256 {
		return errors.New("launch permit executable mismatch")
	}
	// Recheck lifetime/lock after I/O. PID reuse and released ownership refuse.
	start, err = processStart(p.PID)
	if err != nil || start != p.Start {
		return errors.New("launch worker changed during validation")
	}
	return ownsTransactionLock(state, p.PID)
}
func readLaunch(ctx context.Context, paths nativePaths) (bool, error) {
	state, err := openProtectedRoot(ctx, paths.State)
	if err != nil {
		return false, err
	}
	defer state.Close()
	if _, err := state.Lstat("launch.json"); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := protectedFile(state, "launch.json"); err != nil {
		return false, err
	}
	bin, err := openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return false, err
	}
	defer bin.Close()
	updater, err := openProtectedRoot(ctx, filepath.Dir(paths.Updater))
	if err != nil {
		return false, err
	}
	defer updater.Close()
	if err := protectedFile(updater, filepath.Base(paths.Updater)); err != nil {
		return false, err
	}
	return readLaunchAt(ctx, paths, state, bin)
}

// readLaunchAt is the production permit reader after fixed protected-root and
// ownership checks. It is shared with recovery selection and file fixtures.
func readLaunchPermit(ctx context.Context, state *os.Root) (launchPermit, bool, error) {
	var p launchPermit
	b, err := readChecked(ctx, state, "launch.json", 2048)
	if errors.Is(err, os.ErrNotExist) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	info, err := state.Lstat("launch.json")
	if err != nil {
		return p, false, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode().Perm() != 0600 || !ok || stat.Nlink != 1 {
		return p, false, errors.New("unsafe native launch permit mode or links")
	}
	if strictJSON(b, &p) != nil || p.Format != 1 || p.PID < 1 || !validHash(p.StageID) || !validHash(p.Nonce) || !validHash(p.SHA256) || (p.Phase != "candidate" && p.Phase != "rollback" && p.Phase != "aborted") {
		return p, false, errors.New("malformed native launch permit")
	}
	if ticks, err := strconv.ParseUint(p.Start, 10, 64); err != nil || ticks == 0 {
		return p, false, errors.New("malformed native launch start time")
	}
	return p, true, nil
}
func readLaunchAt(ctx context.Context, paths nativePaths, state, bin *os.Root) (bool, error) {
	p, present, err := readLaunchPermit(ctx, state)
	if err != nil || !present {
		return false, err
	}
	// Proven stale identity denies the bypass but leaves ordinary serialized
	// recovery available. Inspection failures are still ambiguous and refuse.
	start, err := processStart(p.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if start != p.Start {
		return false, nil
	}
	actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", p.PID))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := os.Stat(paths.Updater)
	if err != nil {
		return false, err
	}
	if !os.SameFile(actual, expected) {
		return false, nil
	}
	owner, err := transactionLockOwner(state)
	if err != nil {
		return false, err
	}
	if owner != p.PID {
		return false, nil
	}
	if err := validateLaunch(ctx, paths, state, bin, p); err != nil {
		return false, err
	}
	return true, nil
}

func recoverLinux(ctx context.Context, paths nativePaths) error {
	fenceState, err := openProtectedRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer fenceState.Close()
	if err := refuseLinuxInstallerState(ctx, fenceState); err != nil {
		return err
	}
	permitted, err := readLaunch(ctx, paths)
	if err != nil {
		return err
	}
	if permitted {
		return nil
	}
	state, err := openProtectedRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer state.Close()
	bin, err := openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return err
	}
	defer bin.Close()
	return recoverLinuxAt(ctx, paths, state, bin, linuxDriver{paths}, func(ctx context.Context) error {
		if _, err := LoadNativeTrust(ctx); err != nil {
			return err
		}
		if _, err := readInstalled(ctx, state); err != nil {
			return err
		}
		return protectedFile(bin, paths.Binary)
	})
}

// recoverLinuxAt serializes ordinary recovery. The production wrapper supplies
// only checked fixed roots and the unchanged trust/bootstrap verifier.
func recoverLinuxAt(ctx context.Context, paths nativePaths, state, bin *os.Root, driver nativeDriver, verify func(context.Context) error) error {
	if err := refuseLinuxInstallerState(ctx, state); err != nil {
		return err
	}
	permitted, err := readLaunchAt(ctx, paths, state, bin)
	if err != nil {
		return err
	}
	if permitted {
		return nil
	}
	release, err := launcher.AcquireManager(paths.State)
	if err != nil {
		return errors.New("another native update operation is active")
	}
	defer release()
	if err := refuseLinuxInstallerState(ctx, state); err != nil {
		return err
	}
	if _, err := state.Lstat("activation.json"); err == nil {
		if err := verify(ctx); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	worker := activationWorker{state: state, bin: bin, binary: paths.Binary, driver: driver}
	if err := worker.recover(ctx); err != nil {
		return err
	}
	// Re-read after acquiring ownership and reconciling the journal. Do not
	// delete malformed/unsafe control state or let deletion/fence errors pass.
	_, present, err := readLaunchPermit(ctx, state)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if err := state.Remove("launch.json"); err != nil {
		return err
	}
	return worker.fence(ctx, state)
}

func (d linuxDriver) startWithPermit(ctx context.Context) (retErr error) {
	state, err := openProtectedRoot(ctx, d.paths.State)
	if err != nil {
		return err
	}
	defer state.Close()
	bin, err := openProtectedRoot(ctx, d.paths.Bin)
	if err != nil {
		return err
	}
	defer bin.Close()
	j, err := (&activationWorker{state: state}).load(ctx)
	if err != nil {
		return err
	}
	image, err := launchImage(j)
	if err != nil {
		return err
	}
	start, err := processStart(os.Getpid())
	if err != nil {
		return err
	}
	p := launchPermit{Format: 1, PID: os.Getpid(), Start: start, StageID: j.Request.StageID, Nonce: j.Nonce, Phase: j.Phase, SHA256: image.SHA256}
	if err := validateLaunch(ctx, d.paths, state, bin, p); err != nil {
		return err
	}
	for _, name := range []string{"launch.pending", "launch.json"} {
		if err := state.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return publishLaunch(ctx, state, p, d.Fence, func(ctx context.Context) error {
		_, err := runFixed(ctx, "/usr/bin/systemctl", "start", "minecraft-ui.service")
		return err
	})
}

// publishLaunch has a fixed file contract. Its callbacks make failure boundaries
// testable without invoking an installed service or weakening protected opens.
func publishLaunch(ctx context.Context, state *os.Root, p launchPermit, fence func(context.Context, *os.Root) error, start func(context.Context) error) (retErr error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, name := range []string{"launch.json", "launch.pending"} {
			if err := state.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				retErr = errors.Join(retErr, err)
			}
		}
		retErr = errors.Join(retErr, fence(cleanup, state))
	}()
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := writeFile(ctx, state, "launch.pending", bytesReader(b), int64(len(b)), ""); err != nil {
		return err
	}
	if err := state.Rename("launch.pending", "launch.json"); err != nil {
		return err
	}
	if err := fence(ctx, state); err != nil {
		return err
	}
	return start(ctx)
}
