//go:build linux

package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/installinputs"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
)

const (
	linuxInstallerAccount = "minecraft-ui"
	linuxInstallerData    = "/var/lib/minecraft-ui"
	linuxInstallerState   = "/var/lib/minecraft-ui-update"
	linuxInstallerEnv     = "/etc/minecraft-ui/environment"
	linuxControlDirectory = "selfupdate/control"
	linuxEffectsName      = "effects.json"
	linuxResultName       = "result.json"
	linuxExecutorName     = "executor"
	linuxMaxEffectsBytes  = 128 << 10
)

// linuxInstallerSession is deliberately as narrow as the reviewed Linux
// updater bridge. Production always stores *selfupdate.LinuxInstaller; the
// private interface lets package tests exercise control flow without native
// service or account effects.
type linuxInstallerSession interface {
	OpenState(context.Context) (*os.Root, error)
	SnapshotUpdater(context.Context) (*selfupdate.LinuxUpdaterSnapshot, error)
	LockStoppedManager(context.Context) error
	UnlockManager() error
	ReconcileUpdater(context.Context, *selfupdate.LinuxUpdaterSnapshot) error
	Receipt(context.Context) (*selfupdate.MaintenanceReceipt, error)
	CompareAndSwapInstalled(context.Context, *selfupdate.MaintenanceReceipt, selfupdate.Installed) error
	RemoveInstalledForRollback(context.Context, *selfupdate.MaintenanceReceipt, selfupdate.Installed) error
	Close() error
}

type linuxMaintenanceUpdaterReconciler interface {
	ReconcileUpdaterForMaintenance(context.Context, *selfupdate.LinuxUpdaterSnapshot, selfupdate.MaintenanceChallenge) error
}

type linuxMaintenanceUpdaterPreparer interface {
	PrepareMaintenanceControl(context.Context, *selfupdate.LinuxUpdaterSnapshot) (*selfupdate.LinuxUpdaterSnapshot, error)
}

type linuxInstallerInputs interface {
	Summary() installinputs.Summary
	Members() []candidate.Member
	OpenMember(context.Context, string) (*os.File, error)
	Close() error
}

type linuxNativeHooks struct {
	// All hooks are immutable and private. Nil selects the canonical production
	// operation; no environment or caller-controlled path is consulted.
	stateRoot     string
	dataRoot      string
	testRoot      bool
	account       func(context.Context) (linuxPrincipal, bool, error)
	createUser    func(context.Context) (linuxPrincipal, error)
	service       func(context.Context) (linuxServiceStatus, error)
	stop          func(context.Context) error
	start         func(context.Context) error
	reload        func(context.Context) error
	enabled       func(context.Context, bool) error
	gamesStopped  func(context.Context) error
	probeSchema   func(context.Context, string, linuxPrincipal, int64) error
	ready         func(context.Context, transaction, linuxIntent) error
	health        func(context.Context, Image) error
	updateTrust   func(context.Context) error
	configuration func(context.Context) error
	authority     func() error
	now           func() time.Time
	importInputs  func(context.Context, *os.Root, installinputs.Options) (linuxInstallerInputs, error)
	openRecorded  func(context.Context, *os.Root, installinputs.RecordedOptions) (linuxInstallerInputs, error)
}

type linuxInstallDriver struct {
	session   linuxInstallerSession
	state     *os.Root
	store     *journalStore
	operation *os.Root
	inputs    linuxInstallerInputs
	updater   *selfupdate.LinuxUpdaterSnapshot
	intent    *linuxIntent
	tx        *transaction
	assets    map[linuxAssetRole]*linuxAssetSnapshot
	dataHeld  bool
	hooks     linuxNativeHooks
}

func newLinuxInstallDriver(ctx context.Context) (*linuxInstallDriver, error) {
	session, err := selfupdate.AcquireLinuxInstaller(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire Linux installer session: %w", err)
	}
	d, err := newLinuxInstallDriverWithSession(ctx, session, linuxNativeHooks{})
	if err != nil {
		return nil, errors.Join(err, session.Close())
	}
	return d, nil
}

func newLinuxInstallDriverWithSession(ctx context.Context, session linuxInstallerSession, hooks linuxNativeHooks) (*linuxInstallDriver, error) {
	if ctx == nil || session == nil {
		return nil, errors.New("Linux installer session is unavailable")
	}
	if hooks.stateRoot == "" {
		hooks.stateRoot = linuxInstallerState
	}
	if hooks.dataRoot == "" {
		hooks.dataRoot = linuxInstallerData
	}
	if hooks.now == nil {
		hooks.now = func() time.Time { return time.Now().UTC() }
	}
	state, err := session.OpenState(ctx)
	if err != nil {
		return nil, fmt.Errorf("open protected installer state: %w", err)
	}
	store, err := newJournalStoreWithConfig(ctx, state, journalStoreConfig{testRoot: hooks.testRoot})
	if err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("open Linux installer journal: %w", err)
	}
	return &linuxInstallDriver{session: session, state: state, store: store, assets: make(map[linuxAssetRole]*linuxAssetSnapshot), hooks: hooks}, nil
}

func (d *linuxInstallDriver) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.dataHeld {
		errs = append(errs, d.session.UnlockManager())
		d.dataHeld = false
	}
	if d.inputs != nil {
		errs = append(errs, d.inputs.Close())
		d.inputs = nil
	}
	d.updater = nil // the LinuxInstaller owns and closes captured updater snapshots.
	for role, snapshot := range d.assets {
		errs = append(errs, snapshot.Close())
		delete(d.assets, role)
	}
	if d.operation != nil {
		errs = append(errs, d.operation.Close())
		d.operation = nil
	}
	if d.store != nil {
		errs = append(errs, d.store.Close())
		d.store = nil
	}
	if d.state != nil {
		errs = append(errs, d.state.Close())
		d.state = nil
	}
	if d.session != nil {
		errs = append(errs, d.session.Close())
		d.session = nil
	}
	return errors.Join(errs...)
}

func (d *linuxInstallDriver) Load(ctx context.Context) (*transaction, error) {
	if d == nil || d.store == nil {
		return nil, errors.New("Linux installer is closed")
	}
	tx, err := d.store.Load(ctx)
	if err != nil || tx == nil {
		return tx, err
	}
	body, err := d.store.ReadIntent(ctx, *tx)
	if err != nil {
		return nil, err
	}
	intent, err := decodeLinuxIntent(body, *tx)
	if err != nil {
		return nil, err
	}
	op, err := openLinuxOperation(ctx, d.state, tx.ID)
	if err != nil {
		return nil, err
	}
	if intent.PriorInventory != nil && intent.PriorInventory.Status == "retained" {
		proof := &linuxInstallDriver{state: d.state, store: d.store, operation: op, intent: &intent, hooks: d.hooks}
		if _, err := proof.retainedInventoryOrigin(ctx, *intent.PriorInventory); err != nil {
			_ = op.Close()
			return nil, errors.New("retained failed-install retry evidence is no longer valid")
		}
	}
	if d.operation != nil {
		_ = d.operation.Close()
	}
	d.operation, d.intent, d.tx = op, &intent, cloneTransaction(tx)
	return cloneTransaction(tx), nil
}

func (d *linuxInstallDriver) Record(ctx context.Context, prior *transaction, next transaction) error {
	if err := d.store.Record(ctx, prior, next); err != nil {
		return err
	}
	d.tx = cloneTransaction(&next)
	return nil
}

func (d *linuxInstallDriver) StageExecutor(ctx context.Context, tx transaction) (string, error) {
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return "", err
	}
	if d.operation == nil {
		d.operation, err = openLinuxOperation(ctx, d.state, tx.ID)
		if err != nil {
			return "", err
		}
	}
	path := filepath.Join(d.hooks.stateRoot, installDirectory, tx.ID, linuxExecutorName)
	if info, statErr := d.operation.Lstat(linuxExecutorName); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0755 || !isNativeOwned(info) || info.Size() < 1 || info.Size() > candidate.MaxBinaryBytes {
			return "", errors.New("private installer executor copy is unsafe")
		}
		file, err := d.operation.Open(linuxExecutorName)
		if err != nil {
			return "", err
		}
		_, digest, hashErr := hashExecutor(ctx, file)
		_ = file.Close()
		if hashErr != nil || digest != intent.ExecutorSHA256 {
			return "", errors.New("private installer executor copy does not match intent")
		}
		return path, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if d.store.snapshot != nil && d.store.snapshot.present {
		return "", errors.New("prepared private executor disappeared and cannot be recreated during recovery")
	}
	source, err := d.executorSource(ctx, tx, intent)
	if err != nil {
		return "", err
	}
	defer source.Close()
	dst, err := d.operation.OpenFile(linuxExecutorName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return "", fmt.Errorf("create private installer executor: %w", err)
	}
	created, statErr := dst.Stat()
	if statErr != nil || !created.Mode().IsRegular() || !isNativeOwned(created) || created.Mode().Perm() != 0755 {
		_ = dst.Close()
		return "", errors.Join(errors.New("new private executor is unsafe"), statErr)
	}
	h := sha256.New()
	count, copyErr := io.Copy(io.MultiWriter(dst, h), &contextReader{ctx: ctx, r: source})
	syncErr := error(nil)
	if copyErr == nil && count > 0 && count <= candidate.MaxBinaryBytes {
		syncErr = dst.Chmod(0755)
		if syncErr == nil {
			syncErr = dst.Sync()
		}
	} else if copyErr == nil {
		copyErr = errors.New("executor size is outside its bound")
	}
	closeErr := dst.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != intent.ExecutorSHA256 {
		return "", fmt.Errorf("stage verified private executor: %w", errors.Join(copyErr, syncErr, closeErr))
	}
	if err := syncDirectory(d.operation); err != nil {
		return "", fmt.Errorf("sync private executor directory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return path, nil
}

func (d *linuxInstallDriver) VerifyExecutor(ctx context.Context, tx transaction) error {
	if d.hooks.authority != nil {
		if err := d.hooks.authority(); err != nil {
			return err
		}
	} else if os.Geteuid() != 0 {
		return errors.New("root authority is required for Linux installer worker")
	}
	intent, err := d.intentFor(ctx, tx)
	if err != nil {
		return err
	}
	if d.operation == nil {
		d.operation, err = openLinuxOperation(ctx, d.state, tx.ID)
		if err != nil {
			return err
		}
	}
	executorInfo, err := d.operation.Lstat(linuxExecutorName)
	if err != nil || !executorInfo.Mode().IsRegular() || executorInfo.Mode().Perm() != 0755 || !isNativeOwned(executorInfo) {
		return errors.New("private installer executor identity is unavailable")
	}
	executorFile, err := d.operation.Open(linuxExecutorName)
	if err != nil {
		return err
	}
	openedExecutor, statErr := executorFile.Stat()
	if statErr != nil || !os.SameFile(executorInfo, openedExecutor) {
		_ = executorFile.Close()
		return errors.New("private installer executor changed before worker verification")
	}
	_, executorDigest, hashErr := hashExecutor(ctx, executorFile)
	closeExecutorErr := executorFile.Close()
	if hashErr != nil || closeExecutorErr != nil || executorDigest != intent.ExecutorSHA256 {
		return errors.Join(errors.New("private installer executor hash differs from immutable intent"), hashErr, closeExecutorErr)
	}
	info, err := os.Stat("/proc/self/exe")
	if err != nil || info.Mode()&0111 == 0 {
		return errors.New("installer worker executable identity is unavailable")
	}
	exe, err := os.Open("/proc/self/exe")
	if err != nil {
		return errors.New("installer worker executable cannot be opened")
	}
	defer exe.Close()
	current, err := os.Stat("/proc/self/exe")
	if err != nil || !os.SameFile(info, current) || !os.SameFile(info, executorInfo) {
		return errors.New("installer worker executable changed during verification")
	}
	_, digest, err := hashExecutor(ctx, exe)
	if err != nil || digest != intent.ExecutorSHA256 {
		return errors.New("installer worker executable does not match immutable intent")
	}
	return nil
}

func (d *linuxInstallDriver) intentFor(ctx context.Context, tx transaction) (linuxIntent, error) {
	if d.intent != nil && d.tx != nil && d.tx.ID == tx.ID {
		if err := validateLinuxIntent(*d.intent, tx); err == nil {
			return *d.intent, nil
		}
	}
	body, err := d.store.ReadIntent(ctx, tx)
	if err != nil {
		return linuxIntent{}, err
	}
	intent, err := decodeLinuxIntent(body, tx)
	if err != nil {
		return linuxIntent{}, err
	}
	d.intent, d.tx = &intent, cloneTransaction(&tx)
	return intent, nil
}

func openLinuxOperation(ctx context.Context, state *os.Root, id string) (*os.Root, error) {
	if !lowerHex(id, 64) {
		return nil, errors.New("installer operation identity is invalid")
	}
	install, err := state.OpenRoot(installDirectory)
	if err != nil {
		return nil, fmt.Errorf("open installer operation parent: %w", err)
	}
	defer install.Close()
	op, err := install.OpenRoot(id)
	if err != nil {
		return nil, fmt.Errorf("open installer operation: %w", err)
	}
	if err := checkPrivateOperationDir(op); err != nil {
		_ = op.Close()
		return nil, err
	}
	return op, nil
}

func (d *linuxInstallDriver) executorSource(ctx context.Context, tx transaction, intent linuxIntent) (*os.File, error) {
	if tx.Mode == modeApply {
		if d.inputs == nil {
			return nil, errors.New("authenticated executor input is unavailable")
		}
		return d.inputs.OpenMember(ctx, "selfupdate")
	}
	for _, plan := range intent.Assets {
		if plan.Role == linuxAssetUpdater && plan.Before.Present {
			return d.openPreimage("updater")
		}
	}
	return nil, errors.New("retained updater executor is unavailable")
}

func (d *linuxInstallDriver) openPreimage(name string) (*os.File, error) {
	if d.operation == nil || !filepath.IsLocal(name) || name == "." || name == ".." {
		return nil, errors.New("installer preimage name is invalid")
	}
	root, err := d.operation.OpenRoot("preimage")
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func hashExecutor(ctx context.Context, file *os.File) (int64, string, error) {
	if file == nil {
		return 0, "", errors.New("executor file is unavailable")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, "", err
	}
	h := sha256.New()
	count, err := io.Copy(h, &contextReader{ctx: ctx, r: file})
	if _, seekErr := file.Seek(0, io.SeekStart); seekErr != nil {
		err = errors.Join(err, seekErr)
	}
	if err != nil || count < 1 || count > candidate.MaxBinaryBytes {
		return count, "", errors.Join(errors.New("executor hash is outside its bound"), err)
	}
	return count, hex.EncodeToString(h.Sum(nil)), nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func isRootOwned(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0 && st.Nlink == 1 && info.Mode().IsRegular()
}

func isNativeOwned(info os.FileInfo) bool {
	return isRootOwned(info)
}

var _ driver = (*linuxInstallDriver)(nil)
