//go:build linux

package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	launchLinuxStatePath    = "/var/lib/minecraft-ui-update"
	launchLinuxManagerPath  = "/usr/local/bin/minecraft-ui"
	launchLinuxExecutorName = "executor"
	launchLinuxPermitName   = "installation-launch.json"
	launchLinuxPendingName  = "installation-launch.pending"
	launchLinuxMaxPermit    = 2048
	launchLinuxMaxManager   = 256 << 20
)

var errLinuxInstallerRecoveryRequired = errors.New("installer recovery is required before manager startup")

type linuxInstallerLaunchPermit struct {
	Format            int    `json:"format"`
	OperationID       string `json:"operation_id"`
	IntentSHA256      string `json:"intent_sha256"`
	Phase             phase  `json:"phase"`
	ImageSHA256       string `json:"image_sha256"`
	ExecutorSHA256    string `json:"executor_sha256"`
	PID               int    `json:"pid"`
	ProcessStartTicks string `json:"process_start_ticks"`
}

// launchLinuxConfig is private and used only to provide isolated filesystem and
// process fixtures. The exported startup API always uses the canonical paths.
type launchLinuxConfig struct {
	stateRoot     string
	manager       string
	procRoot      string
	uid           uint32
	authority     func() error
	testRoot      bool
	processStart  func(int) (string, error)
	processExe    func(int, os.FileInfo) error
	readProc      func(string, int64) ([]byte, error)
	beforePublish func() error
	afterPublish  func() error
}

type launchLinuxConfigKey struct{}

func withLaunchLinuxConfig(ctx context.Context, config launchLinuxConfig) context.Context {
	return context.WithValue(ctx, launchLinuxConfigKey{}, config)
}

func launchLinuxConfigFrom(ctx context.Context) launchLinuxConfig {
	if ctx != nil {
		if config, ok := ctx.Value(launchLinuxConfigKey{}).(launchLinuxConfig); ok {
			return config
		}
	}
	return launchLinuxConfig{
		stateRoot: launchLinuxStatePath,
		manager:   launchLinuxManagerPath,
		procRoot:  "/proc",
		uid:       0,
		authority: func() error {
			if os.Geteuid() != 0 {
				return errors.New("root authority is required for Linux installer launch checks")
			}
			return nil
		},
	}
}

func grantLinuxInstallerLaunch(ctx context.Context, tx transaction, image Image) error {
	config := launchLinuxConfigFrom(ctx)
	if err := launchLinuxAuthority(config); err != nil {
		return err
	}
	return grantLinuxInstallerLaunchAt(ctx, config, tx, image)
}

func revokeLinuxInstallerLaunch(ctx context.Context, tx transaction) error {
	config := launchLinuxConfigFrom(ctx)
	if err := launchLinuxAuthority(config); err != nil {
		return err
	}
	return revokeLinuxInstallerLaunchAt(ctx, config, tx)
}

func CheckLinuxInstallerStartup(ctx context.Context) (bool, error) {
	config := launchLinuxConfigFrom(ctx)
	if err := launchLinuxAuthority(config); err != nil {
		return false, err
	}
	return checkLinuxInstallerStartupAt(ctx, config)
}

func launchLinuxAuthority(config launchLinuxConfig) error {
	if config.authority != nil {
		return config.authority()
	}
	if os.Geteuid() != 0 {
		return errors.New("root authority is required for Linux installer launch checks")
	}
	return nil
}

func grantLinuxInstallerLaunchAt(ctx context.Context, config launchLinuxConfig, tx transaction, image Image) error {
	if err := launchLinuxContext(ctx); err != nil {
		return err
	}
	state, err := openLaunchLinuxState(ctx, config)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	defer state.Close()
	store, err := newJournalStoreWithConfig(ctx, state, journalStoreConfig{testRoot: config.testRoot})
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	defer store.Close()
	current, err := store.Load(ctx)
	if err != nil || current == nil || !sameLaunchTransaction(*current, tx) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("installer journal differs from requested launch"), err))
	}
	if err := validateLaunchLinuxJournalFiles(state, config.uid, current.ID); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	intentRaw, err := store.ReadIntent(ctx, *current)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	intent, err := decodeLinuxIntent(intentRaw, *current)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	boundImage, ok := linuxInstallerLaunchImage(*current)
	if !ok || *boundImage != image {
		return errLinuxRecoveryRequired(errors.New("installer journal phase does not authorize requested image"))
	}
	if err := validateLaunchLinuxBindings(ctx, config, *current, intent, image); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	permit, err := makeLinuxLaunchPermit(config, *current, intent, image)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	raw, err := marshalLinuxLaunchPermit(permit)
	if err != nil {
		return err
	}
	if _, err := state.Lstat(launchLinuxPendingName); err == nil {
		return errLinuxRecoveryRequired(errors.New("installer launch publication is pending recovery"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errLinuxRecoveryRequired(err)
	}
	if present, err := existingLinuxLaunchPermit(ctx, state, config.uid); present || err != nil {
		if err != nil {
			return errLinuxRecoveryRequired(err)
		}
		oldRaw, _, readErr := readLinuxLaunchPermit(ctx, state, config.uid)
		if readErr == nil && bytes.Equal(oldRaw, raw) && validateLaunchLinuxPermitLive(ctx, config, state, *current, intent, permit, image) == nil {
			return nil
		}
		return errLinuxRecoveryRequired(errors.New("existing installer launch permit is foreign or no longer valid"))
	}
	callerBefore, callerAfter := config.beforePublish, config.afterPublish
	config.beforePublish = func() error {
		if err := revalidateLinuxInstallerLaunch(ctx, store, config, *current, intent, image); err != nil {
			return err
		}
		if callerBefore != nil {
			return callerBefore()
		}
		return nil
	}
	config.afterPublish = func() error {
		if err := revalidateLinuxInstallerLaunch(ctx, store, config, *current, intent, image); err != nil {
			return err
		}
		if callerAfter != nil {
			return callerAfter()
		}
		return nil
	}
	if err := publishLinuxLaunchPermit(ctx, state, raw, config); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	return nil
}

func checkLinuxInstallerStartupAt(ctx context.Context, config launchLinuxConfig) (bool, error) {
	if err := launchLinuxContext(ctx); err != nil {
		return false, err
	}
	if err := verifyLaunchLinuxAncestors(config.stateRoot, config.uid, config.testRoot); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, errLinuxRecoveryRequired(err)
	}
	stateInfo, err := os.Lstat(config.stateRoot)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	if err := validateLaunchLinuxDirectory(stateInfo, config.uid, true); err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	state, err := os.OpenRoot(config.stateRoot)
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	defer state.Close()
	if err := verifyLaunchLinuxRoot(state, stateInfo, config.uid, true); err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	journalInfo, journalErr := state.Lstat(journalName)
	permitInfo, permitErr := state.Lstat(launchLinuxPermitName)
	pendingInfo, pendingErr := state.Lstat(launchLinuxPendingName)
	journalPendingInfo, journalPendingErr := state.Lstat(pendingName)
	_ = journalInfo
	_ = permitInfo
	_ = pendingInfo
	_ = journalPendingInfo
	if pendingErr == nil {
		return false, errLinuxRecoveryRequired(errors.New("installer launch publication is pending recovery"))
	}
	if !errors.Is(pendingErr, os.ErrNotExist) {
		return false, errLinuxRecoveryRequired(pendingErr)
	}
	if journalPendingErr == nil {
		return false, errLinuxRecoveryRequired(errors.New("installer journal publication is pending recovery"))
	}
	if !errors.Is(journalPendingErr, os.ErrNotExist) {
		return false, errLinuxRecoveryRequired(journalPendingErr)
	}
	if journalErr != nil && !errors.Is(journalErr, os.ErrNotExist) {
		return false, errLinuxRecoveryRequired(journalErr)
	}
	if permitErr != nil && !errors.Is(permitErr, os.ErrNotExist) {
		return false, errLinuxRecoveryRequired(permitErr)
	}
	if errors.Is(journalErr, os.ErrNotExist) {
		if errors.Is(permitErr, os.ErrNotExist) {
			installInfo, installErr := state.Lstat(installDirectory)
			if installErr == nil {
				if err := validateLaunchLinuxDirectory(installInfo, config.uid, true); err != nil {
					return false, errLinuxRecoveryRequired(err)
				}
				install, err := state.OpenRoot(installDirectory)
				if err != nil {
					return false, errLinuxRecoveryRequired(err)
				}
				dir, openErr := install.Open(".")
				var entries []string
				var readErr error
				if openErr == nil {
					entries, readErr = dir.Readdirnames(-1)
					readErr = errors.Join(readErr, dir.Close())
				} else {
					readErr = openErr
				}
				closeErr := install.Close()
				if readErr != nil || closeErr != nil || len(entries) != 0 {
					return false, errLinuxRecoveryRequired(errors.Join(errors.New("orphan installer operation state"), readErr, closeErr))
				}
			} else if !errors.Is(installErr, os.ErrNotExist) {
				return false, errLinuxRecoveryRequired(installErr)
			}
			return false, nil
		}
		return false, errLinuxRecoveryRequired(errors.New("orphan installer launch permit"))
	}
	store, err := newJournalStoreWithConfig(ctx, state, journalStoreConfig{testRoot: config.testRoot})
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	defer store.Close()
	tx, err := store.Load(ctx)
	if err != nil || tx == nil {
		return false, errLinuxRecoveryRequired(errors.Join(errors.New("installer journal is unavailable"), err))
	}
	if err := validateLaunchLinuxJournalFiles(state, config.uid, tx.ID); err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	intentRaw, err := store.ReadIntent(ctx, *tx)
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	intent, err := decodeLinuxIntent(intentRaw, *tx)
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	rawPermit, permitInfo, err := readLinuxLaunchPermit(ctx, state, config.uid)
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	permit, err := decodeLinuxLaunchPermit(rawPermit)
	if err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	image, ok := linuxInstallerLaunchImage(*tx)
	if !ok || !linuxLaunchPermitBinds(permit, *tx, intent, *image) {
		return false, errLinuxRecoveryRequired(errors.New("installer launch permit does not bind current journal"))
	}
	if err := validateLaunchLinuxPermitLive(ctx, config, state, *tx, intent, permit, *image); err != nil {
		return false, errLinuxRecoveryRequired(err)
	}
	latestPermit, latestInfo, err := readLinuxLaunchPermit(ctx, state, config.uid)
	if err != nil || !os.SameFile(permitInfo, latestInfo) || !bytes.Equal(rawPermit, latestPermit) {
		return false, errLinuxRecoveryRequired(errors.Join(errors.New("installer launch permit changed during startup validation"), err))
	}
	return true, nil
}

func revokeLinuxInstallerLaunchAt(ctx context.Context, config launchLinuxConfig, tx transaction) error {
	if err := launchLinuxContext(ctx); err != nil {
		return err
	}
	state, err := openLaunchLinuxState(ctx, config)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	defer state.Close()
	store, err := newJournalStoreWithConfig(ctx, state, journalStoreConfig{testRoot: config.testRoot})
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	defer store.Close()
	current, err := store.Load(ctx)
	if err != nil || current == nil || !sameImmutableTransaction(*current, tx) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("installer journal does not match permit revocation"), err))
	}
	if err := validateLaunchLinuxJournalFiles(state, config.uid, current.ID); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	intentRaw, err := store.ReadIntent(ctx, *current)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	intent, err := decodeLinuxIntent(intentRaw, *current)
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	if err := validateLaunchLinuxInvoker(ctx, config, *current, intent); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	raw, info, err := readLinuxLaunchPermit(ctx, state, config.uid)
	if errors.Is(err, os.ErrNotExist) {
		if _, pendingErr := state.Lstat(launchLinuxPendingName); errors.Is(pendingErr, os.ErrNotExist) {
			return nil
		}
		return errLinuxRecoveryRequired(errors.New("unknown installer launch pending entry"))
	}
	if err != nil {
		return errLinuxRecoveryRequired(err)
	}
	permit, err := decodeLinuxLaunchPermit(raw)
	if err != nil || !linuxLaunchPermitSameOperation(permit, *current, intent) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("launch permit belongs to another installer operation"), err))
	}
	if _, err := state.Lstat(launchLinuxPendingName); err == nil {
		return errLinuxRecoveryRequired(errors.New("unknown installer launch pending entry"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return errLinuxRecoveryRequired(err)
	}
	if err := store.verifyRoot(ctx); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	latestTx, err := store.Load(ctx)
	if err != nil || latestTx == nil || !sameLaunchTransaction(*latestTx, *current) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("installer journal changed before permit revocation; preserved"), err))
	}
	latestIntent, err := store.ReadIntent(ctx, *latestTx)
	if err != nil || !bytes.Equal(intentRaw, latestIntent) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("installer intent changed before permit revocation; preserved"), err))
	}
	if err := validateLaunchLinuxInvoker(ctx, config, *current, intent); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	latestRaw, latestInfo, err := readLinuxLaunchPermit(ctx, state, config.uid)
	if err != nil || !os.SameFile(info, latestInfo) || !bytes.Equal(raw, latestRaw) {
		return errLinuxRecoveryRequired(errors.Join(errors.New("installer launch permit changed before revocation; preserved"), err))
	}
	if err := state.Remove(launchLinuxPermitName); err != nil {
		return errLinuxRecoveryRequired(err)
	}
	if err := syncDirectory(state); err != nil {
		return errLinuxRecoveryRequired(fmt.Errorf("sync installer launch permit revocation: %w", err))
	}
	return nil
}

func linuxInstallerLaunchImage(tx transaction) (*Image, bool) {
	switch tx.Phase {
	case phaseVerifying:
		if tx.Target != nil {
			return tx.Target, true
		}
	case phaseRollingBack:
		if tx.Prior != nil {
			return tx.Prior, true
		}
	}
	return nil, false
}

func sameLaunchTransaction(a, b transaction) bool {
	return sameImmutableTransaction(a, b) && a.Phase == b.Phase && a.FailedStage == b.FailedStage && a.Format == b.Format
}

func errLinuxRecoveryRequired(cause error) error {
	_ = cause
	return errLinuxInstallerRecoveryRequired
}

func launchLinuxContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Linux installer launch context is unavailable")
	}
	return ctx.Err()
}

func openLaunchLinuxState(ctx context.Context, config launchLinuxConfig) (*os.Root, error) {
	if err := launchLinuxContext(ctx); err != nil {
		return nil, err
	}
	if err := verifyLaunchLinuxAncestors(config.stateRoot, config.uid, config.testRoot); err != nil {
		return nil, err
	}
	info, err := os.Lstat(config.stateRoot)
	if err != nil {
		return nil, err
	}
	if err := validateLaunchLinuxDirectory(info, config.uid, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(config.stateRoot)
	if err != nil {
		return nil, err
	}
	if err := verifyLaunchLinuxRoot(root, info, config.uid, true); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func verifyLaunchLinuxAncestors(path string, uid uint32, testRoot bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("installer state path is not canonical")
	}
	var ancestors []string
	for current := path; ; current = filepath.Dir(current) {
		ancestors = append(ancestors, current)
		if current == "/" {
			break
		}
	}
	missing := false
	for i := len(ancestors) - 1; i >= 0; i-- {
		current := ancestors[i]
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in installer state path")
		}
		if current == path {
			if err := validateLaunchLinuxDirectory(info, uid, true); err != nil {
				return err
			}
		} else if !testRoot {
			if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
				return errors.New("installer state ancestor permissions are unsafe")
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != 0 {
				return errors.New("installer state ancestor owner is unsafe")
			}
		}
	}
	if missing {
		return os.ErrNotExist
	}
	return nil
}

func validateLaunchLinuxDirectory(info os.FileInfo, uid uint32, private bool) error {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 || private && info.Mode().Perm() != 0700 {
		return errors.New("installer launch directory permissions are unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid {
		return errors.New("installer launch directory owner is unsafe")
	}
	return nil
}

func validateLaunchLinuxJournalFiles(state *os.Root, uid uint32, id string) error {
	journalInfo, err := state.Lstat(journalName)
	if err != nil || validateLaunchLinuxFile(journalInfo, uid, 0600, 1) != nil {
		return errors.Join(errors.New("installer transaction journal file is unsafe"), err)
	}
	install, err := state.OpenRoot(installDirectory)
	if err != nil {
		return err
	}
	defer install.Close()
	if err := validateLaunchLinuxDirectoryFromRoot(install, uid); err != nil {
		return err
	}
	installInfo, err := install.Stat(".")
	installPathInfo, pathErr := state.Lstat(installDirectory)
	if err != nil || pathErr != nil || !os.SameFile(installInfo, installPathInfo) {
		return errors.Join(errors.New("installer intent directory identity changed"), err, pathErr)
	}
	operation, err := install.OpenRoot(id)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := validateLaunchLinuxDirectoryFromRoot(operation, uid); err != nil {
		return err
	}
	operationInfo, err := operation.Stat(".")
	operationPathInfo, pathErr := install.Lstat(id)
	if err != nil || pathErr != nil || !os.SameFile(operationInfo, operationPathInfo) {
		return errors.Join(errors.New("installer operation directory identity changed"), err, pathErr)
	}
	intentInfo, err := operation.Lstat(intentFileName)
	if err != nil || validateLaunchLinuxFile(intentInfo, uid, 0600, 1) != nil {
		return errors.Join(errors.New("installer intent file is unsafe"), err)
	}
	return nil
}

func validateLaunchLinuxDirectoryFromRoot(root *os.Root, uid uint32) error {
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	return validateLaunchLinuxDirectory(info, uid, true)
}

func verifyLaunchLinuxRoot(root *os.Root, expected os.FileInfo, uid uint32, private bool) error {
	info, err := root.Stat(".")
	if err != nil || expected == nil || !os.SameFile(expected, info) {
		return errors.Join(errors.New("installer launch root identity changed"), err)
	}
	return validateLaunchLinuxDirectory(info, uid, private)
}

func readLinuxLaunchPermit(ctx context.Context, state *os.Root, uid uint32) ([]byte, os.FileInfo, error) {
	if err := launchLinuxContext(ctx); err != nil {
		return nil, nil, err
	}
	info, err := state.Lstat(launchLinuxPermitName)
	if err != nil {
		return nil, nil, err
	}
	if err := validateLaunchLinuxFile(info, uid, 0600, 1); err != nil || info.Size() > launchLinuxMaxPermit {
		return nil, nil, errors.Join(errors.New("installer launch permit file is unsafe"), err)
	}
	file, err := state.Open(launchLinuxPermitName)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, nil, errors.Join(errors.New("installer launch permit handle changed"), err)
	}
	body, readErr := io.ReadAll(io.LimitReader(file, launchLinuxMaxPermit+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := state.Lstat(launchLinuxPermitName)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || len(body) > launchLinuxMaxPermit || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) {
		return nil, nil, errors.Join(errors.New("installer launch permit changed while reading"), readErr, statErr, closeErr, pathErr)
	}
	if err := validateLaunchLinuxFile(pathAfter, uid, 0600, 1); err != nil {
		return nil, nil, err
	}
	return body, info, nil
}

func existingLinuxLaunchPermit(ctx context.Context, state *os.Root, uid uint32) (bool, error) {
	_, _, err := readLinuxLaunchPermit(ctx, state, uid)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func decodeLinuxLaunchPermit(raw []byte) (linuxInstallerLaunchPermit, error) {
	var permit linuxInstallerLaunchPermit
	if err := decodeLinuxStrictJSON(raw, launchLinuxMaxPermit, &permit); err != nil {
		return permit, errors.New("malformed installer launch permit")
	}
	if permit.Format != 1 || !lowerHex(permit.OperationID, 64) || !lowerHex(permit.IntentSHA256, 64) || !lowerHex(permit.ImageSHA256, 64) || !lowerHex(permit.ExecutorSHA256, 64) || permit.PID < 1 || (permit.Phase != phaseVerifying && permit.Phase != phaseRollingBack) {
		return permit, errors.New("malformed installer launch permit")
	}
	if ticks, err := strconv.ParseUint(permit.ProcessStartTicks, 10, 64); err != nil || ticks == 0 {
		return permit, errors.New("malformed installer launch process identity")
	}
	return permit, nil
}

func marshalLinuxLaunchPermit(permit linuxInstallerLaunchPermit) ([]byte, error) {
	raw, err := json.Marshal(permit)
	if err != nil {
		return nil, err
	}
	if _, err := decodeLinuxLaunchPermit(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func linuxLaunchPermitBinds(permit linuxInstallerLaunchPermit, tx transaction, intent linuxIntent, image Image) bool {
	return permit.Format == 1 && permit.OperationID == tx.ID && permit.IntentSHA256 == tx.IntentSHA256 && permit.Phase == tx.Phase && permit.ImageSHA256 == image.SHA256 && permit.ExecutorSHA256 == intent.ExecutorSHA256
}

func linuxLaunchPermitSameOperation(permit linuxInstallerLaunchPermit, tx transaction, intent linuxIntent) bool {
	if permit.Format != 1 || permit.OperationID != tx.ID || permit.IntentSHA256 != tx.IntentSHA256 || permit.ExecutorSHA256 != intent.ExecutorSHA256 {
		return false
	}
	switch permit.Phase {
	case phaseVerifying:
		return tx.Target != nil && permit.ImageSHA256 == tx.Target.SHA256
	case phaseRollingBack:
		return tx.Prior != nil && permit.ImageSHA256 == tx.Prior.SHA256
	default:
		return false
	}
}

func makeLinuxLaunchPermit(config launchLinuxConfig, tx transaction, intent linuxIntent, image Image) (linuxInstallerLaunchPermit, error) {
	start, err := launchLinuxProcessStart(config, os.Getpid())
	if err != nil {
		return linuxInstallerLaunchPermit{}, err
	}
	return linuxInstallerLaunchPermit{Format: 1, OperationID: tx.ID, IntentSHA256: tx.IntentSHA256, Phase: tx.Phase, ImageSHA256: image.SHA256, ExecutorSHA256: intent.ExecutorSHA256, PID: os.Getpid(), ProcessStartTicks: start}, nil
}

func validateLaunchLinuxBindings(ctx context.Context, config launchLinuxConfig, tx transaction, intent linuxIntent, image Image) error {
	if tx.Phase != phaseVerifying && tx.Phase != phaseRollingBack {
		return errors.New("installer phase does not permit manager launch")
	}
	managerInfo, managerHash, err := hashLaunchLinuxPath(ctx, config.manager, config.uid, 0755, config.testRoot)
	if err != nil || managerHash != image.SHA256 {
		return errors.Join(errors.New("installed manager image does not match launch journal"), err)
	}
	_ = managerInfo
	operationPath := filepath.Join(config.stateRoot, installDirectory, tx.ID)
	operation, err := openLaunchLinuxPrivateDirectory(operationPath, config.uid)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := validateLaunchLinuxExecutor(ctx, config, operation, intent.ExecutorSHA256); err != nil {
		return err
	}
	if err := validateLaunchLinuxInvoker(ctx, config, tx, intent); err != nil {
		return err
	}
	managerAfter, managerHashAfter, err := hashLaunchLinuxPath(ctx, config.manager, config.uid, 0755, config.testRoot)
	if err != nil || managerHashAfter != image.SHA256 || !os.SameFile(managerInfo, managerAfter) {
		return errors.Join(errors.New("installed manager identity changed during launch validation"), err)
	}
	return nil
}

func revalidateLinuxInstallerLaunch(ctx context.Context, store *journalStore, config launchLinuxConfig, tx transaction, intent linuxIntent, image Image) error {
	latest, err := store.Load(ctx)
	if err != nil || latest == nil || !sameLaunchTransaction(*latest, tx) {
		return errors.Join(errors.New("installer journal changed during launch permit publication"), err)
	}
	raw, err := store.ReadIntent(ctx, *latest)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != latest.IntentSHA256 {
		return errors.New("installer intent changed during launch permit publication")
	}
	verifiedIntent, err := decodeLinuxIntent(raw, *latest)
	if err != nil || verifiedIntent.ExecutorSHA256 != intent.ExecutorSHA256 {
		return errors.Join(errors.New("installer intent identity changed during launch permit publication"), err)
	}
	state, err := openLaunchLinuxState(ctx, config)
	if err != nil {
		return err
	}
	defer state.Close()
	return validateLaunchLinuxBindings(ctx, config, *latest, verifiedIntent, image)
}

func validateLaunchLinuxInvoker(ctx context.Context, config launchLinuxConfig, tx transaction, intent linuxIntent) error {
	operation, err := openLaunchLinuxPrivateDirectory(filepath.Join(config.stateRoot, installDirectory, tx.ID), config.uid)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := validateLaunchLinuxExecutor(ctx, config, operation, intent.ExecutorSHA256); err != nil {
		return err
	}
	start, err := launchLinuxProcessStart(config, os.Getpid())
	if err != nil {
		return err
	}
	if err := matchLaunchLinuxProcessExe(config, os.Getpid(), operation, launchLinuxExecutorName); err != nil {
		return err
	}
	if err := launchLinuxOwnsUpdaterLock(ctx, config, tx, os.Getpid()); err != nil {
		return err
	}
	startAfter, err := launchLinuxProcessStart(config, os.Getpid())
	if err != nil || startAfter != start {
		return errors.Join(errors.New("installer worker changed during launch validation"), err)
	}
	if err := launchLinuxOwnsUpdaterLock(ctx, config, tx, os.Getpid()); err != nil {
		return err
	}
	return nil
}

func validateLaunchLinuxExecutor(ctx context.Context, config launchLinuxConfig, operation *os.Root, expectedHash string) error {
	info, err := operation.Lstat(launchLinuxExecutorName)
	if err != nil || validateLaunchLinuxFile(info, config.uid, 0755, 1) != nil {
		return errors.Join(errors.New("installer executor file is unsafe"), err)
	}
	hash, err := hashLaunchLinuxRoot(ctx, operation, launchLinuxExecutorName, info)
	if err != nil || hash != expectedHash {
		return errors.Join(errors.New("installer executor digest does not match intent"), err)
	}
	return nil
}

func validateLaunchLinuxPermitLive(ctx context.Context, config launchLinuxConfig, state *os.Root, tx transaction, intent linuxIntent, permit linuxInstallerLaunchPermit, image Image) error {
	if !linuxLaunchPermitBinds(permit, tx, intent, image) {
		return errors.New("installer launch permit does not match current transaction")
	}
	start, err := launchLinuxProcessStart(config, permit.PID)
	if err != nil || start != permit.ProcessStartTicks {
		return errors.Join(errors.New("installer launch worker is no longer live"), err)
	}
	operation, err := openLaunchLinuxPrivateDirectory(filepath.Join(config.stateRoot, installDirectory, tx.ID), config.uid)
	if err != nil {
		return err
	}
	defer operation.Close()
	if err := validateLaunchLinuxExecutor(ctx, config, operation, intent.ExecutorSHA256); err != nil {
		return err
	}
	if err := matchLaunchLinuxProcessExe(config, permit.PID, operation, launchLinuxExecutorName); err != nil {
		return err
	}
	if err := launchLinuxOwnsUpdaterLock(ctx, config, tx, permit.PID); err != nil {
		return err
	}
	managerInfo, managerHash, err := hashLaunchLinuxPath(ctx, config.manager, config.uid, 0755, config.testRoot)
	if err != nil || managerHash != image.SHA256 {
		return errors.Join(errors.New("installed manager image changed during startup validation"), err)
	}
	startAfter, err := launchLinuxProcessStart(config, permit.PID)
	if err != nil || startAfter != permit.ProcessStartTicks {
		return errors.Join(errors.New("installer launch worker changed during validation"), err)
	}
	if err := launchLinuxOwnsUpdaterLock(ctx, config, tx, permit.PID); err != nil {
		return err
	}
	managerAfter, managerHashAfter, err := hashLaunchLinuxPath(ctx, config.manager, config.uid, 0755, config.testRoot)
	if err != nil || managerHashAfter != image.SHA256 || !os.SameFile(managerInfo, managerAfter) {
		return errors.Join(errors.New("installed manager identity changed during startup validation"), err)
	}
	return nil
}

func launchLinuxOwnsUpdaterLock(ctx context.Context, config launchLinuxConfig, tx transaction, pid int) error {
	if err := launchLinuxContext(ctx); err != nil {
		return err
	}
	if err := verifyLaunchLinuxAncestors(config.stateRoot, config.uid, config.testRoot); err != nil {
		return err
	}
	rootInfo, err := os.Lstat(config.stateRoot)
	if err != nil || validateLaunchLinuxDirectory(rootInfo, config.uid, true) != nil {
		return errors.Join(errors.New("installer updater lock root is unsafe"), err)
	}
	lockRoot, err := os.OpenRoot(config.stateRoot)
	if err != nil {
		return err
	}
	defer lockRoot.Close()
	if err := verifyLaunchLinuxRoot(lockRoot, rootInfo, config.uid, true); err != nil {
		return err
	}
	info, err := lockRoot.Lstat("manager.lock")
	if err != nil || validateLaunchLinuxFile(info, config.uid, 0600, 1) != nil {
		return errors.Join(errors.New("native updater lock file is unsafe"), err)
	}
	lockFile, err := lockRoot.Open("manager.lock")
	if err != nil {
		return err
	}
	defer lockFile.Close()
	openedInfo, err := lockFile.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || validateLaunchLinuxFile(openedInfo, config.uid, 0600, 1) != nil {
		return errors.Join(errors.New("native updater lock handle changed"), err)
	}
	if err := verifyLaunchLinuxUpdaterLockIdentity(config, lockRoot, rootInfo, lockFile, info); err != nil {
		return err
	}
	stat, ok := openedInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("native updater lock identity is unavailable")
	}
	locks, err := launchLinuxRead(config, "locks", 1<<20)
	if err != nil {
		return err
	}
	if err := verifyLaunchLinuxUpdaterLockIdentity(config, lockRoot, rootInfo, lockFile, info); err != nil {
		return err
	}
	device := fmt.Sprintf("%02x:%02x:%d", unix.Major(uint64(stat.Dev)), unix.Minor(uint64(stat.Dev)), stat.Ino)
	owner, count, err := parseLaunchLinuxLockOwner(locks, device)
	if err != nil || count != 1 || owner != pid {
		return errors.Join(errors.New("installer worker does not hold the actual manager updater lock"), err)
	}
	_ = tx
	return nil
}

func verifyLaunchLinuxUpdaterLockIdentity(config launchLinuxConfig, root *os.Root, expectedRoot os.FileInfo, lockFile *os.File, expectedLock os.FileInfo) error {
	if err := verifyLaunchLinuxAncestors(config.stateRoot, config.uid, config.testRoot); err != nil {
		return errors.Join(errors.New("native updater lock root changed"), err)
	}
	rootFD, err := root.Stat(".")
	if err != nil || !os.SameFile(expectedRoot, rootFD) || validateLaunchLinuxDirectory(rootFD, config.uid, true) != nil {
		return errors.Join(errors.New("native updater lock root handle changed"), err)
	}
	rootPath, err := os.Lstat(config.stateRoot)
	if err != nil || !os.SameFile(expectedRoot, rootPath) || validateLaunchLinuxDirectory(rootPath, config.uid, true) != nil {
		return errors.Join(errors.New("native updater lock root path changed"), err)
	}
	lockPath, err := root.Lstat("manager.lock")
	if err != nil || !os.SameFile(expectedLock, lockPath) || validateLaunchLinuxFile(lockPath, config.uid, 0600, 1) != nil {
		return errors.Join(errors.New("native updater lock path changed"), err)
	}
	lockFD, err := lockFile.Stat()
	if err != nil || !os.SameFile(expectedLock, lockFD) || validateLaunchLinuxFile(lockFD, config.uid, 0600, 1) != nil {
		return errors.Join(errors.New("native updater lock handle changed"), err)
	}
	return nil
}

func parseLaunchLinuxLockOwner(body []byte, device string) (owner, count int, err error) {
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 || fields[1] != "FLOCK" || fields[2] != "ADVISORY" || fields[3] != "WRITE" || fields[5] != device || fields[6] != "0" || fields[7] != "EOF" {
			continue
		}
		candidate, parseErr := strconv.Atoi(fields[4])
		if parseErr != nil || candidate < 1 {
			return 0, count, errors.New("native manager lock owner is ambiguous")
		}
		owner, count = candidate, count+1
	}
	if count > 1 {
		return 0, count, errors.New("native manager lock owner is ambiguous")
	}
	return owner, count, nil
}

func launchLinuxProcessStart(config launchLinuxConfig, pid int) (string, error) {
	if pid < 1 {
		return "", errors.New("invalid installer worker PID")
	}
	if config.processStart != nil {
		return config.processStart(pid)
	}
	body, err := launchLinuxRead(config, filepath.Join(strconv.Itoa(pid), "stat"), 8192)
	if err != nil {
		return "", err
	}
	close := strings.LastIndexByte(string(body), ')')
	if close < 0 {
		return "", errors.New("invalid installer worker process stat")
	}
	fields := strings.Fields(string(body[close+1:]))
	if len(fields) < 20 {
		return "", errors.New("incomplete installer worker process stat")
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", errors.New("invalid installer worker process start time")
	}
	return fields[19], nil
}

func matchLaunchLinuxProcessExe(config launchLinuxConfig, pid int, operation *os.Root, name string) error {
	expected, err := operation.Stat(name)
	if err != nil {
		return err
	}
	if config.processExe != nil {
		return config.processExe(pid, expected)
	}
	actual, err := os.Stat(filepath.Join(config.procRoot, strconv.Itoa(pid), "exe"))
	if err != nil || !os.SameFile(actual, expected) {
		return errors.Join(errors.New("installer worker executable inode does not match protected executor"), err)
	}
	return nil
}

func publishLinuxLaunchPermit(ctx context.Context, state *os.Root, raw []byte, config launchLinuxConfig) error {
	if err := launchLinuxContext(ctx); err != nil {
		return err
	}
	file, err := state.OpenFile(launchLinuxPendingName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, statErr := file.Stat()
	if statErr != nil || validateLaunchLinuxFile(info, config.uid, 0600, 1) != nil {
		file.Close()
		return errors.Join(errors.New("installer launch pending file is unsafe"), statErr)
	}
	if err := writeAll(file, raw); err != nil {
		file.Close()
		return errors.Join(err, cleanupLaunchLinuxPending(state, info, config.uid))
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return errors.Join(fmt.Errorf("sync installer launch pending file: %w", err), cleanupLaunchLinuxPending(state, info, config.uid))
	}
	if err := file.Close(); err != nil {
		return errors.Join(err, cleanupLaunchLinuxPending(state, info, config.uid))
	}
	if err := syncDirectory(state); err != nil {
		return errors.Join(fmt.Errorf("sync installer launch pending entry: %w", err), cleanupLaunchLinuxPending(state, info, config.uid))
	}
	if config.beforePublish != nil {
		if err := config.beforePublish(); err != nil {
			return errors.Join(err, cleanupLaunchLinuxPending(state, info, config.uid))
		}
	}
	if err := state.Link(launchLinuxPendingName, launchLinuxPermitName); err != nil {
		return errors.Join(err, cleanupLaunchLinuxPending(state, info, config.uid))
	}
	if err := syncDirectory(state); err != nil {
		return fmt.Errorf("sync installer launch permit publication: %w", err)
	}
	if config.afterPublish != nil {
		if err := config.afterPublish(); err != nil {
			return fmt.Errorf("installer launch permit publication is durable but uncertain: %w", err)
		}
	}
	permitInfo, err := state.Lstat(launchLinuxPermitName)
	if err != nil || !os.SameFile(info, permitInfo) || validateLaunchLinuxFile(permitInfo, config.uid, 0600, 2) != nil {
		return errors.Join(errors.New("published installer launch permit identity is uncertain"), err)
	}
	permitBytes, err := readLaunchLinuxPinnedEntry(state, launchLinuxPermitName, info, raw, config.uid, 2)
	if err != nil || !bytes.Equal(permitBytes, raw) {
		return errors.Join(errors.New("published installer launch permit content is uncertain"), err)
	}
	pendingInfo, err := state.Lstat(launchLinuxPendingName)
	if err != nil || !os.SameFile(info, pendingInfo) {
		return errors.Join(errors.New("installer launch pending entry changed; preserved"), err)
	}
	if err := state.Remove(launchLinuxPendingName); err != nil {
		return fmt.Errorf("published installer launch permit but could not remove pending link: %w", err)
	}
	if err := syncDirectory(state); err != nil {
		return fmt.Errorf("sync installer launch pending cleanup: %w", err)
	}
	return nil
}

func cleanupLaunchLinuxPending(state *os.Root, expected os.FileInfo, uid uint32) error {
	info, err := state.Lstat(launchLinuxPendingName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(info, expected) || validateLaunchLinuxFile(info, uid, 0600, 1) != nil {
		return errors.New("installer launch pending entry changed; preserved")
	}
	if err := state.Remove(launchLinuxPendingName); err != nil {
		return err
	}
	if err := syncDirectory(state); err != nil {
		return fmt.Errorf("sync removed installer launch pending entry: %w", err)
	}
	return nil
}

func readLaunchLinuxPinnedEntry(root *os.Root, name string, expected os.FileInfo, expectedBytes []byte, uid uint32, links uint64) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, expected) || validateLaunchLinuxFile(info, uid, 0600, links) != nil {
		return nil, errors.Join(errors.New("installer launch entry changed; preserved"), err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, errors.Join(errors.New("installer launch entry handle changed; preserved"), err)
	}
	body, readErr := io.ReadAll(io.LimitReader(file, launchLinuxMaxPermit+1))
	openedAfter, statErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := root.Lstat(name)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || !bytes.Equal(body, expectedBytes) || !os.SameFile(info, openedAfter) || !os.SameFile(info, pathAfter) || validateLaunchLinuxFile(pathAfter, uid, 0600, links) != nil {
		return nil, errors.Join(errors.New("installer launch entry content changed; preserved"), readErr, statErr, closeErr, pathErr)
	}
	return body, nil
}

func openLaunchLinuxPrivateDirectory(path string, uid uint32) (*os.Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := validateLaunchLinuxDirectory(info, uid, true); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	if err := verifyLaunchLinuxRoot(root, info, uid, true); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func validateLaunchLinuxFile(info os.FileInfo, uid uint32, mode os.FileMode, links uint64) error {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode {
		return errors.New("installer launch file permissions are unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uid || stat.Nlink != links {
		return errors.New("installer launch file owner or link count is unsafe")
	}
	return nil
}

func hashLaunchLinuxPath(ctx context.Context, path string, uid uint32, mode os.FileMode, testRoot bool) (os.FileInfo, string, error) {
	rootPath := filepath.Dir(path)
	if err := verifyLaunchLinuxPathAncestors(rootPath, uid, testRoot); err != nil {
		return nil, "", err
	}
	name := filepath.Base(path)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if err != nil || validateLaunchLinuxFile(info, uid, mode, 1) != nil {
		return nil, "", errors.Join(errors.New("installed manager file is unsafe"), err)
	}
	hash, err := hashLaunchLinuxRoot(ctx, root, name, info)
	return info, hash, err
}

func verifyLaunchLinuxPathAncestors(path string, uid uint32, testRoot bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("installer launch file path is not canonical")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !testRoot && info.Mode().Perm()&0022 != 0 {
			return errors.New("installer launch file ancestor is unsafe")
		}
		if !testRoot {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != uid {
				return errors.New("installer launch file ancestor owner is unsafe")
			}
		}
		if current == "/" {
			return nil
		}
	}
}

func hashLaunchLinuxRoot(ctx context.Context, root *os.Root, name string, expected os.FileInfo) (string, error) {
	if err := launchLinuxContext(ctx); err != nil {
		return "", err
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) {
		file.Close()
		return "", errors.Join(errors.New("installer launch file handle changed"), err)
	}
	h := sha256.New()
	count, readErr := io.Copy(h, io.LimitReader(file, launchLinuxMaxManager+1))
	after, statErr := file.Stat()
	closeErr := file.Close()
	pathAfter, pathErr := root.Lstat(name)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil || count > launchLinuxMaxManager || !os.SameFile(expected, after) || !os.SameFile(expected, pathAfter) {
		return "", errors.Join(errors.New("installer launch file changed while hashing"), readErr, statErr, closeErr, pathErr)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func launchLinuxRead(config launchLinuxConfig, name string, limit int64) ([]byte, error) {
	if config.readProc != nil {
		return config.readProc(name, limit)
	}
	path := filepath.Join(config.procRoot, name)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(body)) > limit {
		return nil, errors.Join(errors.New("installer process metadata read failed or exceeded bound"), readErr, closeErr)
	}
	return body, nil
}
