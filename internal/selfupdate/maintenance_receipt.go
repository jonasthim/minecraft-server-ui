package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
)

const maintenanceReceiptLimit = 2048

var errMaintenanceClosed = errors.New("maintenance session is closed")

type maintenanceError struct {
	stage string
	cause error
}

func (e *maintenanceError) Error() string { return "maintenance receipt operation failed: " + e.stage }
func (e *maintenanceError) Unwrap() error { return e.cause }

func maintenanceFail(stage string, cause error) error {
	return &maintenanceError{stage: stage, cause: cause}
}

type maintenanceHooks struct {
	paths              nativePaths
	authorized         func() error
	openProtectedRoot  func(context.Context, string) (*os.Root, error)
	openSafeRoot       func(context.Context, string) (*os.Root, error)
	checkProtectedFile func(*os.Root, string) error
	acquireLock        func(string) (func(), error)
	fence              func(context.Context, *os.Root) error
	writePending       func(context.Context, *os.File, []byte) error
	beforePublish      func(context.Context, *os.Root) error
}

// Maintenance holds the canonical protected roots and the existing updater
// operation lock for one local administrator maintenance session.
type Maintenance struct {
	mu      sync.Mutex
	paths   nativePaths
	state   *os.Root
	bin     *os.Root
	release func()
	pins    []*os.File
	closed  bool
	hooks   maintenanceHooks
}

// MaintenanceReceipt is a receipt snapshot bound to the Maintenance session
// that captured it. Present snapshots retain their original descriptor until
// that session closes.
type MaintenanceReceipt struct {
	owner   *Maintenance
	value   Installed
	present bool
	info    os.FileInfo
	pin     *os.File
	raw     []byte
	digest  string
}

// AcquireMaintenance requires platform administrator authority before opening
// any state or binary paths, then holds the fixed updater lock until Close.
func AcquireMaintenance(ctx context.Context) (*Maintenance, error) {
	return acquireMaintenance(ctx, productionMaintenanceHooks())
}

func acquireMaintenance(ctx context.Context, hooks maintenanceHooks) (_ *Maintenance, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, maintenanceFail("cancelled", err)
	}
	if hooks.authorized == nil || hooks.openProtectedRoot == nil || hooks.openSafeRoot == nil || hooks.checkProtectedFile == nil || hooks.acquireLock == nil || hooks.fence == nil {
		return nil, maintenanceFail("platform authority is unavailable", nil)
	}
	if err := hooks.authorized(); err != nil {
		return nil, maintenanceFail("administrator authority is required", err)
	}
	paths := hooks.paths
	if !canonicalMaintenancePaths(paths) {
		return nil, maintenanceFail("canonical installation paths are unavailable", nil)
	}
	state, err := hooks.openProtectedRoot(ctx, paths.State)
	if err != nil {
		return nil, maintenanceFail("protected update state is unavailable", err)
	}
	bin, err := hooks.openProtectedRoot(ctx, paths.Bin)
	if err != nil {
		return nil, errors.Join(maintenanceFail("protected manager directory is unavailable", err), state.Close())
	}
	if err := hooks.checkProtectedFile(bin, paths.Binary); err != nil {
		return nil, errors.Join(maintenanceFail("canonical manager executable is unsafe", err), bin.Close(), state.Close())
	}
	if err := verifyMaintenanceRootBinding(ctx, hooks, paths.State, state); err != nil {
		return nil, errors.Join(maintenanceFail("protected state binding changed", err), bin.Close(), state.Close())
	}
	if err := verifyMaintenanceRootBinding(ctx, hooks, paths.Bin, bin); err != nil {
		return nil, errors.Join(maintenanceFail("protected manager binding changed", err), bin.Close(), state.Close())
	}
	release, err := hooks.acquireLock(paths.State)
	if err != nil {
		return nil, errors.Join(maintenanceFail("another maintenance operation is active", err), bin.Close(), state.Close())
	}
	m := &Maintenance{paths: paths, state: state, bin: bin, release: release, hooks: hooks}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(maintenanceFail("cancelled", err), m.Close())
	}
	if err := m.verifyBindings(ctx); err != nil {
		return nil, errors.Join(maintenanceFail("protected path binding changed", err), m.Close())
	}
	if err := m.checkConflicts(ctx, false); err != nil {
		return nil, errors.Join(maintenanceFail("pending native state requires reconciliation", err), m.Close())
	}
	return m, nil
}

func canonicalMaintenancePaths(paths nativePaths) bool {
	for _, path := range []string{paths.Data, paths.State, paths.Bin, paths.Binary} {
		if path == "" {
			return false
		}
	}
	return filepath.IsAbs(paths.Data) && filepath.Clean(paths.Data) == paths.Data &&
		filepath.IsAbs(paths.State) && filepath.Clean(paths.State) == paths.State &&
		filepath.IsAbs(paths.Bin) && filepath.Clean(paths.Bin) == paths.Bin &&
		filepath.IsLocal(paths.Binary) && paths.Binary != "." && paths.Binary != ".."
}

func verifyMaintenanceRootBinding(ctx context.Context, hooks maintenanceHooks, path string, held *os.Root) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := hooks.openProtectedRoot(ctx, path)
	if err != nil {
		return err
	}
	defer current.Close()
	before, err := held.Stat(".")
	if err != nil {
		return err
	}
	after, err := current.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return errors.New("protected directory identity changed")
	}
	return nil
}

func (m *Maintenance) verifyBindings(ctx context.Context) error {
	if err := verifyMaintenanceRootBinding(ctx, m.hooks, m.paths.State, m.state); err != nil {
		return err
	}
	return verifyMaintenanceRootBinding(ctx, m.hooks, m.paths.Bin, m.bin)
}

func (m *Maintenance) checkConflicts(ctx context.Context, ownPending bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{"activation.json", "activation.pending", "request.json", "launch.json", "launch.pending", "installed.pending", "installation-receipt.pending"} {
		if ownPending && name == "installation-receipt.pending" {
			continue
		}
		if _, err := m.state.Lstat(name); err == nil {
			return errors.New("native update state requires reconciliation")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	control, err := m.hooks.openSafeRoot(ctx, controlDirectory(m.paths.Data))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer control.Close()
	if _, err := control.Lstat("request.json"); err == nil {
		return errors.New("native update request requires reconciliation")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Receipt returns a strict snapshot of installed.json, or a captured absence.
func (m *Maintenance) Receipt(ctx context.Context) (*MaintenanceReceipt, error) {
	if m == nil {
		return nil, maintenanceFail("maintenance session is unavailable", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, maintenanceFail("maintenance session is closed", errMaintenanceClosed)
	}
	if err := ctx.Err(); err != nil {
		return nil, maintenanceFail("cancelled", err)
	}
	if err := m.verifyBindings(ctx); err != nil {
		return nil, maintenanceFail("protected path binding changed", err)
	}
	if err := m.checkConflicts(ctx, false); err != nil {
		return nil, maintenanceFail("pending native state requires reconciliation", err)
	}
	before, err := m.state.Lstat("installed.json")
	if errors.Is(err, os.ErrNotExist) {
		return &MaintenanceReceipt{owner: m}, nil
	}
	if err != nil {
		return nil, maintenanceFail("installed receipt could not be inspected", err)
	}
	if err := m.hooks.checkProtectedFile(m.state, "installed.json"); err != nil || before.Size() < 1 || before.Size() > maintenanceReceiptLimit {
		return nil, maintenanceFail("installed receipt is unsafe", err)
	}
	file, err := m.state.Open("installed.json")
	if err != nil {
		return nil, maintenanceFail("installed receipt could not be opened", err)
	}
	info, err := file.Stat()
	if err != nil || !os.SameFile(before, info) {
		_ = file.Close()
		return nil, maintenanceFail("installed receipt identity changed", err)
	}
	raw, err := readMaintenanceFile(ctx, file, maintenanceReceiptLimit)
	if err != nil {
		_ = file.Close()
		return nil, maintenanceFail("installed receipt could not be read", err)
	}
	after, statErr := file.Stat()
	pathInfo, pathErr := m.state.Lstat("installed.json")
	if statErr != nil || pathErr != nil || !sameMaintenanceMetadata(info, after) || !os.SameFile(after, pathInfo) || int64(len(raw)) != info.Size() {
		_ = file.Close()
		return nil, maintenanceFail("installed receipt changed while reading", errors.Join(statErr, pathErr))
	}
	var value Installed
	if strictJSON(raw, &value) != nil || !validInstalledReceipt(value) {
		_ = file.Close()
		return nil, maintenanceFail("installed receipt is invalid", nil)
	}
	sum := sha256.Sum256(raw)
	receipt := &MaintenanceReceipt{owner: m, value: value, present: true, info: info, pin: file, raw: bytes.Clone(raw), digest: hex.EncodeToString(sum[:])}
	m.pins = append(m.pins, file)
	return receipt, nil
}

func readMaintenanceFile(ctx context.Context, file *os.File, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(contextReader{ctx, io.NewSectionReader(file, 0, limit+1)}, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("maintenance file exceeds bound")
	}
	return body, nil
}

func sameMaintenanceMetadata(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Size() == after.Size() && before.Mode() == after.Mode() && before.ModTime().Equal(after.ModTime())
}

func sameMaintenanceIdentity(before, after os.FileInfo) bool {
	return before != nil && after != nil && os.SameFile(before, after) && before.Mode().Type() == after.Mode().Type() && before.Mode().Perm() == after.Mode().Perm()
}

func validInstalledReceipt(installed Installed) bool {
	if installed.Format != 1 || installed.Schema < 1 || !validHash(installed.SHA256) {
		return false
	}
	_, err := ParseStable(installed.Version)
	return err == nil
}

func (r *MaintenanceReceipt) Value() (Installed, bool) {
	if r == nil || !r.present {
		return Installed{}, false
	}
	return r.value, true
}

// CompareAndSwapInstalled publishes a validated receipt only if the captured
// receipt snapshot and current canonical manager executable still match.
func (m *Maintenance) CompareAndSwapInstalled(ctx context.Context, prior *MaintenanceReceipt, next Installed) (retErr error) {
	if m == nil {
		return maintenanceFail("maintenance session is unavailable", nil)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return maintenanceFail("maintenance session is closed", errMaintenanceClosed)
	}
	if prior == nil || prior.owner != m {
		return maintenanceFail("prior receipt does not belong to this session", nil)
	}
	if err := ctx.Err(); err != nil {
		return maintenanceFail("cancelled", err)
	}
	if !validInstalledReceipt(next) {
		return maintenanceFail("next receipt is invalid", nil)
	}
	if err := m.verifyBindings(ctx); err != nil {
		return maintenanceFail("protected path binding changed", err)
	}
	if err := m.checkConflicts(ctx, false); err != nil {
		return maintenanceFail("pending native state requires reconciliation", err)
	}
	if err := m.verifyExecutable(ctx, next); err != nil {
		return maintenanceFail("canonical manager executable does not match next receipt", err)
	}
	if err := m.compareCurrentReceipt(ctx, prior); err != nil {
		return maintenanceFail("installed receipt changed since capture", err)
	}
	body, err := json.Marshal(next)
	if err != nil || len(body) == 0 || len(body) > maintenanceReceiptLimit {
		return maintenanceFail("next receipt could not be encoded", err)
	}
	write := m.hooks.writePending
	if write == nil {
		write = writeMaintenancePending
	}
	pending, err := m.state.OpenFile("installation-receipt.pending", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return maintenanceFail("pending receipt could not be created", err)
	}
	pendingInfo, statErr := pending.Stat()
	if statErr != nil || !pendingInfo.Mode().IsRegular() {
		_ = pending.Close()
		return maintenanceFail("pending receipt identity is unsafe", statErr)
	}
	published := false
	defer func() {
		if !published {
			cleanupErr := removeOwnedMaintenancePending(m.state, pending, pendingInfo)
			if cleanupErr != nil {
				retErr = errors.Join(retErr, maintenanceFail("pending receipt cleanup was incomplete", cleanupErr))
			}
			if closeErr := pending.Close(); closeErr != nil {
				retErr = errors.Join(retErr, maintenanceFail("pending receipt close was incomplete", closeErr))
			}
		}
	}()
	if err := write(ctx, pending, body); err != nil {
		return maintenanceFail("pending receipt write was incomplete", err)
	}
	if err := pending.Sync(); err != nil {
		return maintenanceFail("pending receipt sync failed", err)
	}
	if err := m.verifyPending(pending, pendingInfo, body); err != nil {
		return maintenanceFail("pending receipt identity changed", err)
	}
	if m.hooks.beforePublish != nil {
		if err := m.hooks.beforePublish(ctx, m.state); err != nil {
			return maintenanceFail("pending receipt publication was interrupted", err)
		}
	}
	if err := m.verifyBindings(ctx); err != nil {
		return maintenanceFail("protected path binding changed", err)
	}
	if err := m.checkConflicts(ctx, true); err != nil {
		return maintenanceFail("pending native state requires reconciliation", err)
	}
	if err := m.compareCurrentReceipt(ctx, prior); err != nil {
		return maintenanceFail("installed receipt changed before publication", err)
	}
	if err := m.verifyExecutable(ctx, next); err != nil {
		return maintenanceFail("canonical manager executable changed before publication", err)
	}
	if err := m.verifyPending(pending, pendingInfo, body); err != nil {
		return maintenanceFail("pending receipt identity changed before publication", err)
	}
	if err := m.state.Rename("installation-receipt.pending", "installed.json"); err != nil {
		return maintenanceFail("receipt publication failed", err)
	}
	published = true
	m.pins = append(m.pins, pending)
	if err := m.hooks.fence(ctx, m.state); err != nil {
		pending = nil
		return maintenanceFail("publication uncertain", err)
	}
	pending = nil
	return nil
}

func writeMaintenancePending(ctx context.Context, file *os.File, body []byte) error {
	for offset := 0; offset < len(body); {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Write(body[offset:])
		if err != nil {
			return err
		}
		if n <= 0 || n > len(body)-offset {
			return io.ErrShortWrite
		}
		offset += n
	}
	return nil
}

func (m *Maintenance) verifyPending(file *os.File, expected os.FileInfo, body []byte) error {
	info, err := file.Stat()
	if err != nil || !sameMaintenanceIdentity(expected, info) || info.Size() != int64(len(body)) {
		return errors.Join(errors.New("pending receipt handle metadata changed"), err)
	}
	if err := m.hooks.checkProtectedFile(m.state, "installation-receipt.pending"); err != nil {
		return err
	}
	pathInfo, err := m.state.Lstat("installation-receipt.pending")
	if err != nil || !os.SameFile(info, pathInfo) {
		return errors.Join(errors.New("pending receipt path identity changed"), err)
	}
	got, err := readMaintenanceAt(file, int64(len(body)))
	if err != nil || !bytes.Equal(got, body) {
		return errors.Join(errors.New("pending receipt bytes changed"), err)
	}
	return nil
}

func readMaintenanceAt(file *os.File, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(io.NewSectionReader(file, 0, limit+1), limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("maintenance file exceeds bound")
	}
	return body, nil
}

func removeOwnedMaintenancePending(root *os.Root, file *os.File, expected os.FileInfo) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := root.Lstat("installation-receipt.pending")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !sameMaintenanceIdentity(expected, info) || !os.SameFile(expected, pathInfo) {
		return errors.New("foreign pending receipt replacement preserved")
	}
	return root.Remove("installation-receipt.pending")
}

func (m *Maintenance) compareCurrentReceipt(ctx context.Context, prior *MaintenanceReceipt) error {
	current, err := m.state.Lstat("installed.json")
	if prior == nil || prior.owner != m {
		return errors.New("prior receipt does not belong to this maintenance session")
	}
	if !prior.present {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("receipt appeared after captured absence")
	}
	if err != nil {
		return errors.New("captured receipt disappeared")
	}
	if prior.pin == nil || prior.info == nil || !os.SameFile(prior.info, current) {
		return errors.New("receipt path identity changed")
	}
	if err := m.hooks.checkProtectedFile(m.state, "installed.json"); err != nil {
		return err
	}
	held, err := prior.pin.Stat()
	if err != nil || !sameMaintenanceMetadata(prior.info, held) || !os.SameFile(held, current) {
		return errors.Join(errors.New("captured receipt handle changed"), err)
	}
	raw, err := readMaintenanceFile(ctx, prior.pin, maintenanceReceiptLimit)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if !bytes.Equal(raw, prior.raw) || hex.EncodeToString(sum[:]) != prior.digest {
		return errors.New("captured receipt bytes changed")
	}
	after, err := prior.pin.Stat()
	pathInfo, pathErr := m.state.Lstat("installed.json")
	if err != nil || pathErr != nil || !sameMaintenanceMetadata(prior.info, after) || !os.SameFile(after, pathInfo) {
		return errors.Join(errors.New("captured receipt changed during recheck"), err, pathErr)
	}
	return nil
}

func (m *Maintenance) verifyExecutable(ctx context.Context, next Installed) error {
	if err := m.hooks.checkProtectedFile(m.bin, m.paths.Binary); err != nil {
		return err
	}
	hash, err := hashFile(ctx, m.bin, m.paths.Binary)
	if err != nil {
		return err
	}
	if hash != next.SHA256 {
		return errors.New("manager executable hash differs from next receipt")
	}
	return nil
}

// Close releases retained receipt and publication handles, roots, and the
// updater operation lock. It is safe to call more than once.
func (m *Maintenance) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	var closeErr error
	for _, pin := range m.pins {
		if pin != nil {
			closeErr = errors.Join(closeErr, pin.Close())
		}
	}
	m.pins = nil
	if m.bin != nil {
		closeErr = errors.Join(closeErr, m.bin.Close())
		m.bin = nil
	}
	if m.state != nil {
		closeErr = errors.Join(closeErr, m.state.Close())
		m.state = nil
	}
	if m.release != nil {
		m.release()
		m.release = nil
	}
	if closeErr != nil {
		return maintenanceFail("maintenance handles did not close cleanly", closeErr)
	}
	return nil
}
