//go:build linux

package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

type linuxServiceState struct {
	LoadState, ActiveState, SubState string
	MainPID                          int
}

type linuxInstallerHooks struct {
	paths              nativePaths
	authorized         func() error
	openProtectedRoot  func(context.Context, string) (*os.Root, error)
	createStateRoot    func(context.Context, string) (*os.Root, error)
	checkProtectedFile func(*os.Root, string) error
	acquireLock        func(string) (func(), error)
	serviceState       func(context.Context) (linuxServiceState, error)
	openDataRoot       func(context.Context, string) (*os.Root, error)
	managerIDs         func() (int, int, error)
	createDataLock     func(*os.Root, int, int) (*os.File, error)
	checkUpdaterLock   func(*os.Root) error
	fence              func(context.Context, *os.Root) error
	remove             func(*os.Root, string) error
}

type LinuxInstaller struct {
	mu          sync.Mutex
	maintenance *Maintenance
	state, bin  *os.Root
	hooks       linuxInstallerHooks
	dataRelease func()
	dataRoot    *os.Root
	dataLocked  bool
	closed      bool
	snapshots   map[*LinuxUpdaterSnapshot]struct{}
}

type LinuxUpdaterSnapshot struct {
	owner             *LinuxInstaller
	journal           *capturedInstallerFile
	request           *capturedInstallerFile
	previous, next    *capturedInstallerFile
	control           map[string]*capturedInstallerFile
	controlRoot       *os.Root
	controlInfo       os.FileInfo
	controlAbsent     bool
	installed         Installed
	present, terminal bool
	closed            bool
}

type capturedInstallerFile struct {
	name string
	root *os.Root
	file *os.File
	info os.FileInfo
	raw  []byte
	hash string
}

func productionLinuxInstallerHooks() linuxInstallerHooks {
	paths := platformPaths()
	return linuxInstallerHooks{
		paths: paths,
		authorized: func() error {
			if os.Geteuid() != 0 {
				return errMaintenanceAuthority
			}
			return nil
		},
		openProtectedRoot:  openProtectedRoot,
		createStateRoot:    createLinuxInstallerStateRoot,
		checkProtectedFile: protectedFile,
		acquireLock:        launcher.AcquireManager,
		serviceState:       linuxManagerServiceState,
		openDataRoot:       func(ctx context.Context, path string) (*os.Root, error) { return safepath.OpenRoot(ctx, path) },
		managerIDs:         linuxManagerIDs,
		createDataLock:     createLinuxManagerLock,
		checkUpdaterLock:   checkLinuxUpdaterLock,
		fence:              syncPrivilegedRoot,
		remove:             func(root *os.Root, name string) error { return root.Remove(name) },
	}
}

func AcquireLinuxInstaller(ctx context.Context) (*LinuxInstaller, error) {
	return acquireLinuxInstaller(ctx, productionLinuxInstallerHooks())
}

func acquireLinuxInstaller(ctx context.Context, h linuxInstallerHooks) (_ *LinuxInstaller, retErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.authorized == nil || h.openProtectedRoot == nil || h.createStateRoot == nil || h.checkProtectedFile == nil || h.acquireLock == nil || h.serviceState == nil {
		return nil, errors.New("Linux installer authority is unavailable")
	}
	if err := h.authorized(); err != nil {
		return nil, errors.New("administrator authority is required")
	}
	p := h.paths
	if !canonicalMaintenancePaths(p) {
		return nil, errors.New("canonical installation paths are unavailable")
	}
	state, err := h.openProtectedRoot(ctx, p.State)
	if errors.Is(err, os.ErrNotExist) {
		state, err = h.createStateRoot(ctx, p.State)
	}
	if err != nil {
		return nil, fmt.Errorf("protected update state is unavailable: %w", err)
	}
	bin, err := h.openProtectedRoot(ctx, p.Bin)
	if err != nil {
		_ = state.Close()
		return nil, fmt.Errorf("protected manager directory is unavailable: %w", err)
	}
	if _, err := bin.Lstat(p.Binary); err == nil {
		if err = h.checkProtectedFile(bin, p.Binary); err != nil {
			_ = bin.Close()
			_ = state.Close()
			return nil, errors.New("canonical manager executable is unsafe")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = bin.Close()
		_ = state.Close()
		return nil, err
	}
	if err = verifyMaintenanceRootBinding(ctx, hooksAsMaintenance(h), p.State, state); err != nil {
		_ = bin.Close()
		_ = state.Close()
		return nil, err
	}
	if err = verifyMaintenanceRootBinding(ctx, hooksAsMaintenance(h), p.Bin, bin); err != nil {
		_ = bin.Close()
		_ = state.Close()
		return nil, err
	}
	if h.checkUpdaterLock != nil {
		if err = h.checkUpdaterLock(state); err != nil {
			_ = bin.Close()
			_ = state.Close()
			return nil, errors.New("updater operation lock is unsafe")
		}
	}
	release, err := h.acquireLock(p.State)
	if err != nil {
		_ = bin.Close()
		_ = state.Close()
		return nil, fmt.Errorf("another maintenance operation is active: %w", err)
	}
	mh := hooksAsMaintenance(h)
	m := &Maintenance{paths: p, state: state, bin: bin, release: release, hooks: mh}
	s := &LinuxInstaller{maintenance: m, state: state, bin: bin, hooks: h}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := verifyMaintenanceRootBinding(ctx, mh, p.State, state); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := verifyMaintenanceRootBinding(ctx, mh, p.Bin, bin); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if h.checkUpdaterLock != nil {
		if err := h.checkUpdaterLock(state); err != nil {
			return nil, errors.Join(errors.New("updater operation lock changed"), s.Close())
		}
	}
	if err := verifyOptionalBinary(h, bin, p.Binary); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	return s, nil
}

func hooksAsMaintenance(h linuxInstallerHooks) maintenanceHooks {
	return maintenanceHooks{paths: h.paths, authorized: h.authorized, openProtectedRoot: h.openProtectedRoot, openSafeRoot: safepath.OpenRoot, checkProtectedFile: h.checkProtectedFile, acquireLock: h.acquireLock, fence: syncPrivilegedRoot}
}

func verifyOptionalBinary(h linuxInstallerHooks, root *os.Root, name string) error {
	if err := h.checkProtectedFile(root, name); err == nil {
		return nil
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		if _, statErr := root.Lstat(name); errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		return errors.New("canonical manager executable is unsafe")
	}
}

func (s *LinuxInstaller) OpenState(ctx context.Context) (*os.Root, error) {
	if s == nil {
		return nil, errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("installer session is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyMaintenanceRootBinding(ctx, s.maintenance.hooks, s.maintenance.paths.State, s.state); err != nil {
		return nil, err
	}
	clone, err := s.hooks.openProtectedRoot(ctx, s.maintenance.paths.State)
	if err != nil {
		return nil, err
	}
	heldInfo, heldErr := s.state.Stat(".")
	cloneInfo, cloneErr := clone.Stat(".")
	if heldErr != nil || cloneErr != nil || !os.SameFile(heldInfo, cloneInfo) {
		_ = clone.Close()
		return nil, errors.New("opened update state root differs from session root")
	}
	if err = verifyMaintenanceRootBinding(ctx, s.maintenance.hooks, s.maintenance.paths.State, s.state); err != nil {
		_ = clone.Close()
		return nil, err
	}
	return clone, nil
}

func (s *LinuxInstaller) SnapshotUpdater(ctx context.Context) (*LinuxUpdaterSnapshot, error) {
	if s == nil {
		return nil, errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("installer session is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.verify(ctx); err != nil {
		return nil, err
	}
	snap := &LinuxUpdaterSnapshot{owner: s, control: map[string]*capturedInstallerFile{}}
	fail := func(err error) (*LinuxUpdaterSnapshot, error) { _ = snap.close(); return nil, err }
	jf, err := captureInstallerFile(ctx, s.state, "activation.json", 8192, s.hooks.checkProtectedFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(errors.New("activation journal is unsafe"))
	}
	snap.journal = jf
	rf, err := captureInstallerFile(ctx, s.state, "request.json", 2048, s.hooks.checkProtectedFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(errors.New("native request is unsafe"))
	}
	snap.request = rf
	for _, name := range []string{s.maintenance.paths.Binary + ".previous", s.maintenance.paths.Binary + ".next"} {
		f, e := captureInstallerFile(ctx, s.bin, name, MaxArtifactBytes, s.hooks.checkProtectedFile)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return fail(errors.New("updater scratch image is unsafe"))
		}
		if name == s.maintenance.paths.Binary+".previous" {
			snap.previous = f
		} else {
			snap.next = f
		}
	}
	entries, err := installerReadDir(s.bin)
	if err != nil {
		return fail(errors.New("manager binary directory cannot be inspected"))
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, s.maintenance.paths.Binary+".") && name != s.maintenance.paths.Binary+".previous" && name != s.maintenance.paths.Binary+".next" {
			return fail(errors.New("unknown manager scratch file requires review"))
		}
	}
	for _, name := range []string{"activation.pending", "launch.json", "launch.pending", "installed.pending", "installation-receipt.pending"} {
		if _, e := s.state.Lstat(name); e == nil {
			return fail(errors.New("pending updater state requires recovery"))
		} else if !errors.Is(e, os.ErrNotExist) {
			return fail(e)
		}
	}
	if jf == nil {
		if rf != nil || snap.previous != nil || snap.next != nil {
			return fail(errors.New("updater artifacts exist without an activation journal"))
		}
		r, err := s.receiptSnapshot(ctx)
		if err != nil {
			return fail(err)
		}
		if r != nil && r.present {
			if _, statErr := s.bin.Lstat(s.maintenance.paths.Binary); statErr == nil {
				if err = s.maintenance.verifyExecutable(ctx, r.value); err != nil {
					return fail(errors.New("installed manager image differs from receipt"))
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return fail(errors.New("installed manager image differs from receipt"))
			}
			snap.installed = r.value
			snap.present = true
		}
		if err = snapshotControlAbsent(ctx, s); err != nil {
			return fail(err)
		}
		s.registerSnapshot(snap)
		return snap, nil
	}
	var j activationJournal
	if strictJSON(jf.raw, &j) != nil || j.Format != 1 || !validHash(j.Request.StageID) || j.Request.ActorID < 1 || j.Request.RequestedAt.IsZero() || j.Request.RequestedAt.After(time.Now().Add(time.Minute)) || !validHash(j.Nonce) || !validInstalledReceipt(j.Prior) || !validInstalledReceipt(j.Candidate) || j.Prior.Schema != j.Candidate.Schema || !terminalLinuxPhase(j.Phase) || (j.Phase == "rolled_back" && !j.RollbackAttempted) || ((j.Phase == "healthy" || j.Phase == "aborted") && j.RollbackAttempted) {
		return fail(errors.New("activation journal is not a supported terminal record"))
	}
	want := j.Candidate
	if j.Phase != "healthy" {
		want = j.Prior
	}
	r, err := s.receiptSnapshot(ctx)
	if err != nil {
		return fail(err)
	}
	if r == nil || !r.present || r.value != want {
		return fail(errors.New("terminal journal and installed receipt disagree"))
	}
	if err = s.maintenance.verifyExecutable(ctx, want); err != nil {
		return fail(errors.New("terminal journal and manager image disagree"))
	}
	snap.installed = want
	snap.present = true
	snap.terminal = true
	if err = validateTerminalRequest(snap, j); err != nil {
		return fail(err)
	}
	if err = validateTerminalScratch(snap, j); err != nil {
		return fail(err)
	}
	if err = readTerminalControl(ctx, s, j, snap); err != nil {
		return fail(err)
	}
	s.registerSnapshot(snap)
	return snap, nil
}

func terminalLinuxPhase(p string) bool { return p == "healthy" || p == "rolled_back" || p == "aborted" }

func (s *LinuxInstaller) receiptSnapshot(ctx context.Context) (*MaintenanceReceipt, error) {
	// The existing reader rejects any activation journal. Capture this receipt
	// with the same strict JSON and file-pinning checks so the journal, receipt,
	// and executable are validated as one immutable snapshot.
	f, err := captureInstallerFile(ctx, s.state, "installed.json", maintenanceReceiptLimit, s.hooks.checkProtectedFile)
	if errors.Is(err, os.ErrNotExist) {
		return &MaintenanceReceipt{owner: s.maintenance}, nil
	}
	if err != nil {
		return nil, err
	}
	var value Installed
	if strictJSON(f.raw, &value) != nil || !validInstalledReceipt(value) {
		_ = f.file.Close()
		return nil, errors.New("installed receipt is invalid")
	}
	receipt := &MaintenanceReceipt{owner: s.maintenance, value: value, present: true, info: f.info, pin: f.file, raw: bytes.Clone(f.raw), digest: hashBytesHex(f.raw)}
	s.maintenance.pins = append(s.maintenance.pins, f.file)
	return receipt, nil
}

func captureInstallerFile(ctx context.Context, root *os.Root, name string, limit int64, check func(*os.Root, string) error) (*capturedInstallerFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if err = check(root, name); err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("unsafe bounded updater file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		_ = f.Close()
		return nil, errors.New("updater file changed while opening")
	}
	var body []byte
	var digest string
	if limit == MaxArtifactBytes {
		digest, e = hashInstallerHandle(ctx, f, limit)
	} else {
		body, e = readMaintenanceFile(ctx, f, limit)
	}
	if e != nil {
		_ = f.Close()
		return nil, e
	}
	after, e := root.Lstat(name)
	if e != nil || !sameMaintenanceMetadata(opened, mustFileStat(f)) || !os.SameFile(opened, after) || (limit != MaxArtifactBytes && int64(len(body)) != opened.Size()) {
		_ = f.Close()
		return nil, errors.New("updater file changed while reading")
	}
	return &capturedInstallerFile{name: name, root: root, file: f, info: opened, raw: body, hash: digest}, nil
}
func mustFileStat(f *os.File) os.FileInfo { i, _ := f.Stat(); return i }
func hashBytesHex(b []byte) string        { sum := sha256.Sum256(b); return fmt.Sprintf("%x", sum) }
func hashInstallerHandle(ctx context.Context, f *os.File, limit int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(contextReader{ctx, io.NewSectionReader(f, 0, limit+1)}, limit+1))
	if err != nil {
		return "", err
	}
	if n != info.Size() || n > limit {
		return "", errors.New("updater file changed or exceeds bound")
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (f *capturedInstallerFile) verify(ctx context.Context, check func(*os.Root, string) error) error {
	if f == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	i, err := f.file.Stat()
	if err != nil || !sameMaintenanceMetadata(f.info, i) {
		return errors.New("captured updater file changed")
	}
	if err = check(f.root, f.name); err != nil {
		return err
	}
	p, err := f.root.Lstat(f.name)
	if err != nil || !os.SameFile(f.info, p) {
		return errors.New("captured updater path was replaced")
	}
	if f.hash != "" {
		got, err := hashInstallerHandle(ctx, f.file, MaxArtifactBytes)
		if err != nil || got != f.hash {
			return errors.New("captured updater bytes changed")
		}
		after, statErr := f.file.Stat()
		pathInfo, pathErr := f.root.Lstat(f.name)
		if statErr != nil || pathErr != nil || !sameMaintenanceMetadata(f.info, after) || !os.SameFile(after, pathInfo) {
			return errors.New("captured updater file changed during hashing")
		}
		return nil
	}
	raw, err := readMaintenanceFile(ctx, f.file, int64(len(f.raw)))
	if err != nil || !bytes.Equal(raw, f.raw) {
		return errors.New("captured updater bytes changed")
	}
	after, statErr := f.file.Stat()
	pathInfo, pathErr := f.root.Lstat(f.name)
	if statErr != nil || pathErr != nil || !sameMaintenanceMetadata(f.info, after) || !os.SameFile(after, pathInfo) {
		return errors.New("captured updater file changed during reading")
	}
	return nil
}

func validateTerminalRequest(s *LinuxUpdaterSnapshot, j activationJournal) error {
	if s.request == nil {
		return nil
	}
	var request ActivationRequest
	if strictJSON(s.request.raw, &request) != nil || request != j.Request {
		return errors.New("terminal updater request does not match journal")
	}
	return nil
}
func validateTerminalScratch(s *LinuxUpdaterSnapshot, j activationJournal) error {
	for _, f := range []*capturedInstallerFile{s.previous, s.next} {
		if f == nil {
			continue
		}
		sum := f.hash
		if sum == "" {
			sum = hashBytesHex(f.raw)
		}
		if !validHash(sum) || f.info.Size() < 1 {
			return errors.New("terminal updater scratch image is invalid")
		}
		want := j.Candidate.SHA256
		if strings.HasSuffix(f.name, ".previous") {
			want = j.Prior.SHA256
		}
		if sum != want {
			return errors.New("terminal updater scratch image is unrelated to journal")
		}
	}
	return nil
}

func snapshotControlAbsent(ctx context.Context, s *LinuxInstaller) error {
	root, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("manager control state is unavailable")
	}
	defer root.Close()
	entries, err := installerReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("manager control state requires reconciliation")
	}
	return nil
}

func readTerminalControl(ctx context.Context, s *LinuxInstaller, j activationJournal, snap *LinuxUpdaterSnapshot) error {
	root, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
	if errors.Is(err, os.ErrNotExist) {
		snap.controlAbsent = true
		return nil
	}
	if err != nil {
		return errors.New("manager control state is unavailable")
	}
	entries, err := installerReadDir(root)
	if err != nil {
		_ = root.Close()
		return err
	}
	allowed := map[string]bool{"challenge.json": true, "drained.json": true, "request.json": true}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !allowed[entry.Name()] {
			_ = root.Close()
			return errors.New("unrecognized manager control state")
		}
	}
	uid, gid, err := s.hooks.managerIDs()
	if err != nil {
		_ = root.Close()
		return errors.New("manager account is unavailable")
	}
	for _, name := range []string{"challenge.json", "drained.json", "request.json"} {
		f, e := captureInstallerFile(ctx, root, name, 2048, func(r *os.Root, n string) error { return checkManagerControlFile(r, n, uid, gid) })
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			_ = root.Close()
			return errors.New("manager control file is unsafe")
		}
		snap.control[name] = f
	}
	if f := snap.control["challenge.json"]; f != nil {
		var c Challenge
		wantTo := j.Candidate.Version
		if j.Phase == "rolled_back" {
			wantTo = j.Prior.Version
		}
		observedAt := time.Now()
		if strictJSON(f.raw, &c) != nil || c.Format != 1 || c.Nonce != j.Nonce || c.FromVersion != j.Prior.Version || c.ToVersion != wantTo || c.ActorID != j.Request.ActorID || c.ExpiresAt.IsZero() || c.ExpiresAt.Before(j.Request.RequestedAt) || c.ExpiresAt.After(observedAt.Add(10*time.Minute)) {
			_ = root.Close()
			return errors.New("manager challenge does not match terminal journal")
		}
	}
	if f := snap.control["drained.json"]; f != nil {
		var d DrainReceipt
		if strictJSON(f.raw, &d) != nil || d.Format != 1 || d.Nonce != j.Nonce || d.Version != j.Prior.Version || d.Schema != j.Prior.Schema || d.ExecutableSHA256 != j.Prior.SHA256 || !d.Drained || d.CreatedAt.Before(j.Request.RequestedAt) || d.CreatedAt.After(time.Now().Add(time.Minute)) {
			_ = root.Close()
			return errors.New("manager drain receipt does not match terminal journal")
		}
	}
	if f := snap.control["request.json"]; f != nil {
		var r CandidateProbeRequest
		if strictJSON(f.raw, &r) != nil || r.Format != 1 || r.Nonce != j.Nonce || r.Version != j.Candidate.Version || r.Schema != j.Candidate.Schema || r.SHA256 != j.Candidate.SHA256 {
			_ = root.Close()
			return errors.New("manager candidate request does not match terminal journal")
		}
	}
	snap.controlRoot = root
	snap.controlInfo, err = root.Stat(".")
	if err != nil {
		return err
	}
	return nil
}

func checkManagerControlFile(root *os.Root, name string, uid, gid int) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || int(st.Uid) != uid || int(st.Gid) != gid || info.Mode().Perm()&0077 != 0 {
		return errors.New("manager control file ownership or mode is unsafe")
	}
	return nil
}

func installerReadDir(root *os.Root) ([]os.DirEntry, error) {
	f, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

func (s *LinuxInstaller) verify(ctx context.Context) error {
	if err := verifyMaintenanceRootBinding(ctx, s.maintenance.hooks, s.maintenance.paths.State, s.state); err != nil {
		return err
	}
	if err := verifyMaintenanceRootBinding(ctx, s.maintenance.hooks, s.maintenance.paths.Bin, s.bin); err != nil {
		return err
	}
	return nil
}

func (s *LinuxUpdaterSnapshot) Installed() (Installed, bool) {
	if s == nil || s.closed || !s.present {
		return Installed{}, false
	}
	return s.installed, true
}
func (s *LinuxUpdaterSnapshot) HasTerminal() bool { return s != nil && !s.closed && s.terminal }
func (s *LinuxInstaller) registerSnapshot(snapshot *LinuxUpdaterSnapshot) {
	if s.snapshots == nil {
		s.snapshots = map[*LinuxUpdaterSnapshot]struct{}{}
	}
	s.snapshots[snapshot] = struct{}{}
}
func (s *LinuxInstaller) validateSnapshot(snapshot *LinuxUpdaterSnapshot) (Installed, bool, error) {
	if s == nil || snapshot == nil || snapshot.owner != s || snapshot.closed {
		return Installed{}, false, errors.New("updater snapshot does not belong to this live session")
	}
	if _, ok := s.snapshots[snapshot]; !ok {
		return Installed{}, false, errors.New("updater snapshot identity is not owned by this session")
	}
	return snapshot.installed, snapshot.present, nil
}
func (s *LinuxUpdaterSnapshot) close() error {
	if s == nil || s.closed {
		return nil
	}
	s.closed = true
	var err error
	for _, f := range []*capturedInstallerFile{s.journal, s.request, s.previous, s.next} {
		if f != nil && f.file != nil {
			err = errors.Join(err, f.file.Close())
		}
	}
	for _, f := range s.control {
		if f != nil && f.file != nil {
			err = errors.Join(err, f.file.Close())
		}
	}
	if s.controlRoot != nil {
		err = errors.Join(err, s.controlRoot.Close())
	}
	return err
}

func (s *LinuxInstaller) LockStoppedManager(ctx context.Context) error {
	if s == nil {
		return errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("installer session is closed")
	}
	if s.dataLocked {
		return errors.New("manager data lock is already held")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.requireStopped(ctx); err != nil {
		return err
	}
	root, err := s.hooks.openDataRoot(ctx, s.maintenance.paths.Data)
	if err != nil {
		return errors.New("existing manager data directory is required")
	}
	uid, gid, err := s.hooks.managerIDs()
	if err != nil {
		_ = root.Close()
		return errors.New("manager account is unavailable")
	}
	if err = checkManagerDataRoot(root, uid, gid); err != nil {
		_ = root.Close()
		return err
	}
	lockInfo, err := root.Lstat("manager.lock")
	var createdLock *os.File
	if errors.Is(err, os.ErrNotExist) {
		createdLock, err = s.hooks.createDataLock(root, uid, gid)
		if err != nil {
			_ = root.Close()
			return err
		}
		lockInfo, err = createdLock.Stat()
		if err != nil {
			_ = createdLock.Close()
			_ = root.Close()
			return err
		}
	} else if err != nil {
		_ = root.Close()
		return err
	} else if err = checkManagerLock(root, uid, gid); err != nil {
		_ = root.Close()
		return err
	}
	if err = ctx.Err(); err != nil {
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return err
	}
	release, err := s.hooks.acquireLock(s.maintenance.paths.Data)
	if err != nil {
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return fmt.Errorf("manager data lock is unavailable: %w", err)
	}
	current, openErr := s.hooks.openDataRoot(ctx, s.maintenance.paths.Data)
	if openErr != nil {
		release()
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return errors.New("manager data binding changed after lock")
	}
	before, beforeErr := root.Stat(".")
	after, afterErr := current.Stat(".")
	_ = current.Close()
	if beforeErr != nil || afterErr != nil || !os.SameFile(before, after) {
		release()
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return errors.New("manager data binding changed after lock")
	}
	currentLock, lockErr := root.Lstat("manager.lock")
	if lockErr != nil || !os.SameFile(lockInfo, currentLock) {
		release()
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return errors.New("manager data lock file changed during acquisition")
	}
	if err = checkManagerLock(root, uid, gid); err != nil {
		release()
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return err
	}
	if err = s.requireStopped(ctx); err != nil {
		release()
		if createdLock != nil {
			_ = createdLock.Close()
		}
		_ = root.Close()
		return err
	}
	if createdLock != nil {
		if err = createdLock.Close(); err != nil {
			release()
			_ = root.Close()
			return err
		}
	}
	s.dataRelease = release
	s.dataRoot = root
	s.dataLocked = true
	return nil
}
func (s *LinuxInstaller) requireStopped(ctx context.Context) error {
	st, err := s.hooks.serviceState(ctx)
	if err != nil {
		return errors.New("manager service state is unavailable")
	}
	if st.MainPID != 0 {
		return errors.New("manager service is running")
	}
	if st.LoadState != "loaded" && st.LoadState != "not-found" {
		return errors.New("manager service state is ambiguous")
	}
	if st.ActiveState != "inactive" && st.ActiveState != "failed" {
		return errors.New("manager service is not inactive")
	}
	if st.SubState != "dead" && st.SubState != "failed" {
		return errors.New("manager service substate is not stopped")
	}
	if st.LoadState == "not-found" && (st.ActiveState != "inactive" || st.SubState != "dead") {
		return errors.New("missing manager service state is inconsistent")
	}
	return ctx.Err()
}

func (s *LinuxInstaller) UnlockManager() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dataLocked {
		return nil
	}
	release := s.dataRelease
	s.dataRelease = nil
	s.dataLocked = false
	if release != nil {
		release()
	}
	if s.dataRoot != nil {
		err := s.dataRoot.Close()
		s.dataRoot = nil
		return err
	}
	return nil
}

func (s *LinuxInstaller) ReconcileUpdater(ctx context.Context, snapshot *LinuxUpdaterSnapshot) error {
	if s == nil {
		return errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.dataLocked {
		return errors.New("stopped manager data lock is required")
	}
	if _, _, err := s.validateSnapshot(snapshot); err != nil {
		return err
	}
	if !snapshot.terminal {
		return nil
	}
	fence := s.hooks.fence
	if fence == nil {
		fence = syncPrivilegedRoot
	}
	if err := s.requireStopped(ctx); err != nil {
		return err
	}
	if err := s.verify(ctx); err != nil {
		return err
	}
	jf := snapshot.journal
	if jf == nil {
		return errors.New("terminal updater snapshot is incomplete")
	}
	var j activationJournal
	if strictJSON(jf.raw, &j) != nil {
		return errors.New("terminal updater journal is invalid")
	}
	all := []*capturedInstallerFile{snapshot.request, snapshot.previous, snapshot.next}
	for _, f := range all {
		if err := f.verify(ctx, s.hooks.checkProtectedFile); err != nil {
			return err
		}
	}
	if err := jf.verify(ctx, s.hooks.checkProtectedFile); err != nil {
		return err
	}
	if s.hooks.checkUpdaterLock != nil {
		if err := s.hooks.checkUpdaterLock(s.state); err != nil {
			return errors.New("updater operation lock changed")
		}
	}
	for _, name := range []string{"activation.pending", "launch.json", "launch.pending", "installed.pending", "installation-receipt.pending"} {
		if _, err := s.state.Lstat(name); err == nil {
			return errors.New("pending updater state appeared after capture")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if snapshot.request == nil {
		if _, err := s.state.Lstat("request.json"); err == nil {
			return errors.New("native request appeared after capture")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, f := range snapshot.control {
		if f == nil {
			continue
		}
		uid, gid, err := s.hooks.managerIDs()
		if err != nil {
			return err
		}
		if err = f.verify(ctx, func(r *os.Root, n string) error { return checkManagerControlFile(r, n, uid, gid) }); err != nil {
			return err
		}
	}
	for _, name := range []string{s.maintenance.paths.Binary + ".previous", s.maintenance.paths.Binary + ".next"} {
		var captured *capturedInstallerFile
		for _, f := range all {
			if f != nil && f.root == s.bin && f.name == name {
				captured = f
				break
			}
		}
		if captured == nil {
			if _, err := s.bin.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return errors.New("updater scratch image appeared after capture")
			}
		} else if err := captured.verify(ctx, s.hooks.checkProtectedFile); err != nil {
			return err
		}
	}
	if snapshot.controlRoot != nil {
		current, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
		if err != nil {
			return errors.New("manager control root changed")
		}
		oldInfo, oldErr := snapshot.controlRoot.Stat(".")
		newInfo, newErr := current.Stat(".")
		_ = current.Close()
		if oldErr != nil || newErr != nil || !os.SameFile(oldInfo, newInfo) || !os.SameFile(snapshot.controlInfo, oldInfo) {
			return errors.New("manager control root changed")
		}
		entries, err := installerReadDir(snapshot.controlRoot)
		if err != nil {
			return err
		}
		if len(entries) != len(snapshot.control) {
			return errors.New("manager control file set changed")
		}
		for _, entry := range entries {
			if snapshot.control[entry.Name()] == nil {
				return errors.New("manager control file set changed")
			}
		}
	} else if snapshot.controlAbsent {
		current, err := s.hooks.openDataRoot(ctx, controlDirectory(s.maintenance.paths.Data))
		if err == nil {
			_ = current.Close()
			return errors.New("manager control root appeared after capture")
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("manager control root could not be revalidated")
		}
	}
	remove := s.hooks.remove
	if remove == nil {
		remove = func(root *os.Root, name string) error { return root.Remove(name) }
	}
	for _, name := range []string{s.maintenance.paths.Binary + ".previous", s.maintenance.paths.Binary + ".next"} {
		if err := removeCapturedInstallerFile(ctx, s.bin, name, all, s.hooks.checkProtectedFile, remove); err != nil {
			return err
		}
	}
	if snapshot.request != nil {
		if err := snapshot.request.verify(ctx, s.hooks.checkProtectedFile); err != nil {
			return err
		}
		if err := remove(s.state, "request.json"); err != nil {
			return err
		}
	}
	for _, name := range []string{"request.json", "drained.json", "challenge.json"} {
		f := snapshot.control[name]
		if f == nil {
			continue
		}
		uid, gid, err := s.hooks.managerIDs()
		if err != nil {
			return err
		}
		if err = f.verify(ctx, func(r *os.Root, n string) error { return checkManagerControlFile(r, n, uid, gid) }); err != nil {
			return err
		}
		if snapshot.controlRoot == nil {
			return errors.New("captured manager control root is unavailable")
		}
		if err = remove(snapshot.controlRoot, name); err != nil {
			return err
		}
	}
	if err := jf.verify(ctx, s.hooks.checkProtectedFile); err != nil {
		return err
	}
	if err := remove(s.state, "activation.json"); err != nil {
		return err
	}
	if err := fence(ctx, s.bin); err != nil {
		return err
	}
	if err := fence(ctx, s.state); err != nil {
		return err
	}
	if snapshot.controlRoot != nil {
		if err := fence(ctx, snapshot.controlRoot); err != nil {
			return err
		}
	}
	return nil
}
func removeCapturedInstallerFile(ctx context.Context, root *os.Root, name string, files []*capturedInstallerFile, check func(*os.Root, string) error, remove func(*os.Root, string) error) error {
	var found *capturedInstallerFile
	for _, f := range files {
		if f != nil && f.root == root && f.name == name {
			found = f
			break
		}
	}
	if found == nil {
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		return errors.New("unrecognized updater scratch file")
	}
	if err := found.verify(ctx, check); err != nil {
		return err
	}
	return remove(root, name)
}

func (s *LinuxInstaller) Receipt(ctx context.Context) (*MaintenanceReceipt, error) {
	if s == nil {
		return nil, errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("installer session is closed")
	}
	return s.maintenance.Receipt(ctx)
}
func (s *LinuxInstaller) CompareAndSwapInstalled(ctx context.Context, r *MaintenanceReceipt, next Installed) error {
	if s == nil {
		return errors.New("installer session unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.dataLocked {
		return errors.New("stopped manager data lock is required")
	}
	if r == nil || r.owner != s.maintenance {
		return errors.New("receipt does not belong to this session")
	}
	return s.maintenance.CompareAndSwapInstalled(ctx, r, next)
}
func (s *LinuxInstaller) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var closeErr error
	for snapshot := range s.snapshots {
		closeErr = errors.Join(closeErr, snapshot.close())
	}
	if s.dataLocked && s.dataRelease != nil {
		s.dataRelease()
		s.dataRelease = nil
		s.dataLocked = false
	}
	if s.dataRoot != nil {
		closeErr = errors.Join(closeErr, s.dataRoot.Close())
		s.dataRoot = nil
	}
	return errors.Join(closeErr, s.maintenance.Close())
}

func createLinuxInstallerStateRoot(ctx context.Context, path string) (*os.Root, error) {
	if path != "/var/lib/minecraft-ui-update" {
		return nil, errors.New("refusing noncanonical installer state root")
	}
	parent, err := openProtectedRoot(ctx, "/var/lib")
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err = parent.Mkdir("minecraft-ui-update", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err = syncPrivilegedRoot(ctx, parent); err != nil {
		return nil, err
	}
	state, err := openProtectedRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	if err = syncPrivilegedRoot(ctx, state); err != nil {
		_ = state.Close()
		return nil, err
	}
	return state, nil
}
func linuxManagerIDs() (int, int, error) {
	u, err := user.Lookup("minecraft-ui")
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}
func checkManagerDataRoot(root *os.Root, uid, gid int) error {
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int(st.Uid) != uid || int(st.Gid) != gid || info.Mode().Perm() != 0700 {
		return errors.New("manager data directory ownership or mode is unsafe")
	}
	return nil
}
func checkManagerLock(root *os.Root, uid, gid int) error {
	info, err := root.Lstat("manager.lock")
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || int(st.Uid) != uid || int(st.Gid) != gid || info.Mode().Perm()&0022 != 0 {
		return errors.New("manager data lock ownership or mode is unsafe")
	}
	return nil
}
func checkLinuxUpdaterLock(root *os.Root) error {
	info, err := root.Lstat("manager.lock")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("updater lock ownership or mode is unsafe")
	}
	return nil
}
func createLinuxManagerLock(root *os.Root, uid, gid int) (_ *os.File, retErr error) {
	f, err := root.OpenFile("manager.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	ok := false
	defer func() {
		if ok {
			return
		}
		pathInfo, pathErr := root.Lstat("manager.lock")
		if pathErr == nil && os.SameFile(info, pathInfo) {
			if e := root.Remove("manager.lock"); e != nil {
				retErr = errors.Join(retErr, e)
			} else if e := syncRoot(root); e != nil {
				retErr = errors.Join(retErr, e)
			}
		}
		if e := f.Close(); e != nil {
			retErr = errors.Join(retErr, e)
		}
	}()
	if err = f.Chown(uid, gid); err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		return nil, err
	}
	if err = f.Sync(); err != nil {
		return nil, err
	}
	if err = syncRoot(root); err != nil {
		return nil, err
	}
	if err = checkManagerLock(root, uid, gid); err != nil {
		return nil, err
	}
	ok = true
	return f, nil
}
func linuxManagerServiceState(ctx context.Context) (linuxServiceState, error) {
	out, err := runFixed(ctx, "/usr/bin/systemctl", "show", "minecraft-ui.service", "--property=LoadState,ActiveState,SubState,MainPID")
	if err != nil {
		return linuxServiceState{}, err
	}
	if len(out) > 2048 {
		return linuxServiceState{}, errors.New("service state response exceeded bound")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pair := strings.SplitN(line, "=", 2)
		if len(pair) != 2 {
			return linuxServiceState{}, errors.New("malformed service state")
		}
		if _, exists := values[pair[0]]; exists {
			return linuxServiceState{}, errors.New("duplicate service state field")
		}
		values[pair[0]] = pair[1]
	}
	if len(values) != 4 {
		return linuxServiceState{}, errors.New("incomplete service state")
	}
	pid, err := strconv.Atoi(values["MainPID"])
	if err != nil || pid < 0 {
		return linuxServiceState{}, errors.New("invalid service PID")
	}
	return linuxServiceState{LoadState: values["LoadState"], ActiveState: values["ActiveState"], SubState: values["SubState"], MainPID: pid}, nil
}
