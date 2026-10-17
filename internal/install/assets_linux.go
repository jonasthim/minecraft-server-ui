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
	"strings"
	"syscall"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/release/candidate"
	"golang.org/x/sys/unix"
)

type linuxAssetRole string

const (
	linuxAssetManager          linuxAssetRole = "manager"
	linuxAssetUpdater          linuxAssetRole = "updater"
	linuxAssetUnitctl          linuxAssetRole = "unitctl"
	linuxAssetManagerUnit      linuxAssetRole = "manager_unit"
	linuxAssetGameUnit         linuxAssetRole = "game_unit"
	linuxAssetUpdaterUnit      linuxAssetRole = "updater_unit"
	linuxAssetRecoveryUnit     linuxAssetRole = "recovery_unit"
	linuxAssetManagerDropin    linuxAssetRole = "manager_dropin"
	linuxAssetSudoers          linuxAssetRole = "sudoers"
	linuxAssetLicense          linuxAssetRole = "license"
	linuxAssetNotices          linuxAssetRole = "notices"
	linuxAssetDeploymentNotice linuxAssetRole = "deployment_notice"
	linuxAssetBootstrapTrust   linuxAssetRole = "bootstrap_trust"
	linuxAssetUpdateTrust      linuxAssetRole = "update_trust"
)

type linuxAssetSpec struct {
	Role   linuxAssetRole
	Member string
	Path   string
	Mode   uint32
}

var fixedLinuxAssets = [...]linuxAssetSpec{
	{linuxAssetManager, "minecraft-ui", "/usr/local/bin/minecraft-ui", 0755},
	{linuxAssetUpdater, "selfupdate", "/usr/local/lib/minecraft-ui/selfupdate", 0755},
	{linuxAssetUnitctl, "unitctl", "/usr/local/lib/minecraft-ui/unitctl", 0755},
	{linuxAssetManagerUnit, "minecraft-ui.service", "/etc/systemd/system/minecraft-ui.service", 0644},
	{linuxAssetGameUnit, "minecraft@.service", "/etc/systemd/system/minecraft@.service", 0644},
	{linuxAssetUpdaterUnit, "minecraft-ui-update@.service", "/etc/systemd/system/minecraft-ui-update@.service", 0644},
	{linuxAssetRecoveryUnit, "minecraft-ui-update-recovery.service", "/etc/systemd/system/minecraft-ui-update-recovery.service", 0644},
	{linuxAssetManagerDropin, "minecraft-ui-update.conf", "/etc/systemd/system/minecraft-ui.service.d/update.conf", 0644},
	{linuxAssetSudoers, "sudoers", "/etc/sudoers.d/minecraft-ui", 0440},
	{linuxAssetLicense, "LICENSE", "/usr/local/lib/minecraft-ui/LICENSE", 0644},
	{linuxAssetNotices, "THIRD_PARTY_NOTICES.txt", "/usr/local/lib/minecraft-ui/THIRD_PARTY_NOTICES.txt", 0644},
	{linuxAssetDeploymentNotice, "NOTICE", "/usr/local/lib/minecraft-ui/NOTICE", 0644},
	{linuxAssetBootstrapTrust, "", "/etc/minecraft-ui/bootstrap-trust.json", 0644},
	{linuxAssetUpdateTrust, "", "/etc/minecraft-ui/update-trust.json", 0644},
}

type linuxAssetState struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
}

type linuxAssetSnapshot struct {
	role           linuxAssetRole
	state          linuxAssetState
	path           string
	parentPath     string
	boundPath      string
	parentComplete bool
	parent         *os.Root
	pinned         *os.File
}

func (p *linuxAssetSnapshot) State() linuxAssetState {
	if p == nil {
		return linuxAssetState{}
	}
	return p.state
}

func (p *linuxAssetSnapshot) Close() error {
	if p == nil {
		return nil
	}
	var errs []error
	if p.pinned != nil {
		errs = append(errs, p.pinned.Close())
		p.pinned = nil
	}
	if p.parent != nil {
		errs = append(errs, p.parent.Close())
		p.parent = nil
	}
	return errors.Join(errs...)
}

// Private test-only path/identity seam. It is nil in every shipping build and
// has no environment, CLI, or exported control surface.
type linuxAssetTestPolicyConfig struct {
	root         string
	uid          uint32
	paths        map[linuxAssetRole]string
	beforeLink   func(linuxAssetRole) error
	beforeRename func(linuxAssetRole) error
	afterRename  func(linuxAssetRole) error
}

var linuxAssetTestPolicy *linuxAssetTestPolicyConfig

func linuxAssetSpecs() []linuxAssetSpec {
	out := make([]linuxAssetSpec, len(fixedLinuxAssets))
	copy(out, fixedLinuxAssets[:])
	return out
}

func linuxAssetSpecForRole(role linuxAssetRole) (linuxAssetSpec, bool) {
	for _, spec := range fixedLinuxAssets {
		if spec.Role == role {
			return spec, true
		}
	}
	return linuxAssetSpec{}, false
}

func linuxAssetPath(spec linuxAssetSpec) (string, string, error) {
	path := spec.Path
	boundary := string(filepath.Separator)
	if test := linuxAssetTestPolicy; test != nil {
		mapped, ok := test.paths[spec.Role]
		if !ok {
			return "", "", errors.New("Linux asset test path is not mapped")
		}
		path, boundary = mapped, test.root
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !filepath.IsAbs(boundary) || filepath.Clean(boundary) != boundary {
		return "", "", errors.New("Linux asset path is not canonical")
	}
	rel, err := filepath.Rel(boundary, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", "", errors.New("Linux asset path escapes its protected root")
	}
	return path, boundary, nil
}

func linuxAssetExpectedUID() uint32 {
	if linuxAssetTestPolicy != nil {
		return linuxAssetTestPolicy.uid
	}
	return 0
}

func linuxAssetLimit(role linuxAssetRole) int64 {
	if role == linuxAssetManager || role == linuxAssetUpdater || role == linuxAssetUnitctl {
		return candidate.MaxBinaryBytes
	}
	return candidate.MaxAssetBytes
}

func inspectLinuxAsset(ctx context.Context, role linuxAssetRole) (*linuxAssetSnapshot, error) {
	if ctx == nil {
		return nil, errors.New("nil Linux asset context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec, ok := linuxAssetSpecForRole(role)
	if !ok {
		return nil, errors.New("unknown Linux asset role")
	}
	path, boundary, err := linuxAssetPath(spec)
	if err != nil {
		return nil, err
	}
	parentPath := filepath.Dir(path)
	root, boundPath, complete, err := openLinuxAssetParent(ctx, parentPath, boundary)
	if err != nil {
		return nil, err
	}
	snapshot := &linuxAssetSnapshot{role: role, path: path, parentPath: parentPath, boundPath: boundPath, parentComplete: complete, parent: root}
	if !complete {
		return snapshot, nil
	}
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		_ = snapshot.Close()
		return nil, fmt.Errorf("inspect fixed Linux asset: %w", err)
	}
	file, state, err := inspectLinuxAssetFile(ctx, root, name, info, spec)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	snapshot.state, snapshot.pinned = state, file
	return snapshot, nil
}

func openLinuxAssetParent(ctx context.Context, parentPath, boundary string) (*os.Root, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", false, err
	}
	if boundary != string(filepath.Separator) {
		if err := checkLinuxAssetDirectory(boundary); err != nil {
			return nil, "", false, err
		}
	}
	bound := parentPath
	for {
		_, err := os.Lstat(bound)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", false, fmt.Errorf("inspect Linux asset parent: %w", err)
		}
		if bound == boundary || bound == filepath.Dir(bound) {
			return nil, "", false, errors.New("no protected Linux asset ancestor")
		}
		bound = filepath.Dir(bound)
	}
	if !pathWithin(boundary, bound) {
		return nil, "", false, errors.New("Linux asset ancestor escapes protected root")
	}
	if err := checkLinuxAssetDirectoryChain(bound, boundary); err != nil {
		return nil, "", false, err
	}
	root, err := safepath.OpenRoot(ctx, bound)
	if err != nil {
		return nil, "", false, fmt.Errorf("open protected Linux asset parent: %w", err)
	}
	opened, err := root.Stat(".")
	current, currentErr := os.Lstat(bound)
	if err != nil || currentErr != nil || !os.SameFile(opened, current) {
		_ = root.Close()
		return nil, "", false, errors.New("Linux asset parent changed while opening")
	}
	return root, bound, bound == parentPath, nil
}

// createLinuxAssetParents creates only absent path components beneath a held
// protected ancestor. New directory modes are applied through opened directory
// handles so the caller's umask cannot make shared deployment parents
// inaccessible to the manager service account.
func createLinuxAssetParents(ctx context.Context, target, boundary string) error {
	if ctx == nil || !filepath.IsAbs(target) || filepath.Clean(target) != target || !filepath.IsAbs(boundary) || filepath.Clean(boundary) != boundary || !pathWithin(boundary, target) || target == boundary {
		return errors.New("deployment asset parent path is invalid")
	}
	parent, bound, _, err := openLinuxAssetParent(ctx, target, boundary)
	if err != nil {
		return err
	}
	defer func() {
		if parent != nil {
			_ = parent.Close()
		}
	}()
	missing := make([]string, 0, 4)
	for current := target; current != bound; current = filepath.Dir(current) {
		if current == filepath.Dir(current) {
			return errors.New("deployment asset parent escapes protected ancestor")
		}
		if _, err := os.Lstat(current); err == nil {
			return errors.New("deployment asset parent appeared during creation")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
	}
	if len(missing) == 0 {
		return nil
	}
	boundInfo, err := os.Lstat(bound)
	openedBound, openedErr := parent.Stat(".")
	if err != nil || openedErr != nil || !os.SameFile(boundInfo, openedBound) {
		return errors.Join(errors.New("deployment asset ancestor changed before creation"), err, openedErr)
	}
	currentRoot := parent
	parent = nil // currentRoot owns the root from here; it advances below.
	defer func() {
		if currentRoot != nil {
			_ = currentRoot.Close()
		}
	}()
	for i := len(missing) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		leaf := filepath.Base(missing[i])
		if !filepath.IsLocal(leaf) || leaf == "." || leaf == ".." {
			return errors.New("deployment asset directory component is invalid")
		}
		if err := currentRoot.Mkdir(leaf, 0755); err != nil {
			return fmt.Errorf("create fixed asset parent: %w", err)
		}
		child, err := currentRoot.OpenRoot(leaf)
		if err != nil {
			return fmt.Errorf("open newly created asset parent: %w", err)
		}
		created, statErr := child.Stat(".")
		entry, entryErr := currentRoot.Lstat(leaf)
		if statErr != nil || entryErr != nil || !validLinuxAssetDirectoryOwner(created) || created.Mode().Perm()&0022 != 0 || !os.SameFile(created, entry) {
			_ = child.Close()
			return errors.Join(errors.New("new asset parent identity is unsafe"), statErr, entryErr)
		}
		directory, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			return fmt.Errorf("open new asset parent for mode update: %w", err)
		}
		chmodErr := directory.Chmod(0755)
		modeInfo, modeErr := child.Stat(".")
		modeEntry, modeEntryErr := currentRoot.Lstat(leaf)
		closeErr := directory.Close()
		if chmodErr != nil || modeErr != nil || modeEntryErr != nil || closeErr != nil || !validLinuxAssetDirectoryOwner(modeInfo) || modeInfo.Mode().Perm() != 0755 || !os.SameFile(created, modeInfo) || !os.SameFile(created, modeEntry) {
			_ = child.Close()
			return errors.Join(errors.New("new asset parent mode or identity changed"), chmodErr, modeErr, modeEntryErr, closeErr)
		}
		if err := syncDirectory(child); err != nil {
			_ = child.Close()
			return err
		}
		if err := syncDirectory(currentRoot); err != nil {
			_ = child.Close()
			return err
		}
		previous := currentRoot
		currentRoot = child
		if err := previous.Close(); err != nil {
			return err
		}
	}
	actual, err := currentRoot.Stat(".")
	pathInfo, pathErr := os.Lstat(target)
	if err != nil || pathErr != nil || !os.SameFile(actual, pathInfo) {
		return errors.Join(errors.New("created asset parent is no longer bound to its fixed path"), err, pathErr)
	}
	if err := checkLinuxAssetDirectoryChain(target, boundary); err != nil {
		return err
	}
	return ctx.Err()
}

func validLinuxAssetDirectoryOwner(info os.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == linuxAssetExpectedUID()
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func checkLinuxAssetDirectoryChain(path, boundary string) error {
	for current := path; ; current = filepath.Dir(current) {
		if err := checkLinuxAssetDirectory(current); err != nil {
			return err
		}
		if current == boundary || current == filepath.Dir(current) {
			if current != boundary && boundary != string(filepath.Separator) {
				return errors.New("Linux asset ancestry does not reach protected root")
			}
			return nil
		}
	}
}

func checkLinuxAssetDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect Linux asset directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != linuxAssetExpectedUID() || info.Mode().Perm()&0022 != 0 {
		return errors.New("Linux asset directory is not protected and correctly owned")
	}
	return nil
}

func inspectLinuxAssetFile(ctx context.Context, root *os.Root, name string, before os.FileInfo, spec linuxAssetSpec) (*os.File, linuxAssetState, error) {
	var zero linuxAssetState
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm() != os.FileMode(spec.Mode) || before.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != linuxAssetExpectedUID() || stat.Nlink != 1 || before.Size() < 0 || before.Size() > linuxAssetLimit(spec.Role) {
		return nil, zero, errors.New("Linux asset is not a bounded root-owned single-link regular file with its fixed mode")
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, zero, fmt.Errorf("open fixed Linux asset: %w", err)
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, zero, errors.New("Linux asset changed while opening")
	}
	hash, size, err := hashLinuxAsset(ctx, file, linuxAssetLimit(spec.Role))
	if err != nil {
		_ = file.Close()
		return nil, zero, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, zero, err
	}
	after, err := root.Lstat(name)
	if err != nil || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, zero, errors.New("Linux asset changed while hashing")
	}
	stat = opened.Sys().(*syscall.Stat_t)
	return file, linuxAssetState{Present: true, SHA256: hash, Size: size, Mode: uint32(opened.Mode().Perm()), Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func hashLinuxAsset(ctx context.Context, reader io.Reader, limit int64) (string, int64, error) {
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", total, err
		}
		n, err := reader.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return "", total, errors.New("Linux asset exceeds its fixed size bound")
			}
			_, _ = h.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", total, fmt.Errorf("read Linux asset: %w", err)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

func currentLinuxAsset(ctx context.Context, snapshot *linuxAssetSnapshot) (linuxAssetState, error) {
	if snapshot == nil || snapshot.parent == nil {
		return linuxAssetState{}, errors.New("closed or missing Linux asset snapshot")
	}
	path, boundary, err := linuxAssetPath(mustLinuxAssetSpec(snapshot.role))
	if err != nil || path != snapshot.path || filepath.Dir(path) != snapshot.parentPath {
		return linuxAssetState{}, errors.New("Linux asset snapshot binding changed")
	}
	root, boundPath, complete, err := openLinuxAssetParent(ctx, snapshot.parentPath, boundary)
	if err != nil {
		return linuxAssetState{}, err
	}
	defer root.Close()
	if boundPath != snapshot.boundPath || complete != snapshot.parentComplete {
		return linuxAssetState{}, errors.New("Linux asset parent binding changed")
	}
	opened, err := root.Stat(".")
	held, heldErr := snapshot.parent.Stat(".")
	if err != nil || heldErr != nil || !os.SameFile(opened, held) {
		return linuxAssetState{}, errors.New("Linux asset parent identity changed")
	}
	if !complete {
		return linuxAssetState{}, nil
	}
	spec := mustLinuxAssetSpec(snapshot.role)
	info, err := root.Lstat(filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) {
		return linuxAssetState{}, nil
	}
	if err != nil {
		return linuxAssetState{}, err
	}
	file, state, err := inspectLinuxAssetFile(ctx, root, filepath.Base(path), info, spec)
	if err != nil {
		return linuxAssetState{}, err
	}
	_ = file.Close()
	return state, nil
}

func mustLinuxAssetSpec(role linuxAssetRole) linuxAssetSpec {
	spec, _ := linuxAssetSpecForRole(role)
	return spec
}

func checkLinuxAssetSnapshot(ctx context.Context, snapshot *linuxAssetSnapshot) error {
	state, err := currentLinuxAsset(ctx, snapshot)
	if err != nil {
		return err
	}
	if state != snapshot.state {
		return errors.New("Linux asset no longer matches captured state")
	}
	if snapshot.state.Present {
		current, err := snapshot.parent.Lstat(filepath.Base(snapshot.path))
		if err != nil || snapshot.pinned == nil {
			return errors.New("captured Linux asset identity is unavailable")
		}
		pinned, pinnedErr := snapshot.pinned.Stat()
		if pinnedErr != nil || !os.SameFile(pinned, current) {
			return errors.New("Linux asset inode was replaced")
		}
	}
	return ctx.Err()
}

func captureLinuxAsset(ctx context.Context, snapshot *linuxAssetSnapshot, operation *os.Root) error {
	if ctx == nil || operation == nil || snapshot == nil {
		return errors.New("invalid Linux asset capture arguments")
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		return err
	}
	if !snapshot.state.Present {
		return nil
	}
	if err := operation.Mkdir("preimage", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create Linux asset preimage directory: %w", err)
	}
	if err := syncLinuxAssetDir(operation); err != nil {
		return fmt.Errorf("sync Linux asset operation directory: %w", err)
	}
	dir, err := operation.OpenRoot("preimage")
	if err != nil {
		return fmt.Errorf("open Linux asset preimage directory: %w", err)
	}
	defer dir.Close()
	if err := checkPrivateOperationDir(dir); err != nil {
		return err
	}
	name := string(snapshot.role)
	dst, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create exclusive Linux asset preimage: %w", err)
	}
	owned, err := dst.Stat()
	if err != nil {
		_ = dst.Close()
		_ = dir.Remove(name)
		return fmt.Errorf("inspect new Linux asset preimage: %w", err)
	}
	cleanup := func() {
		if owned != nil {
			if current, e := dir.Lstat(name); e == nil && os.SameFile(owned, current) {
				_ = dir.Remove(name)
			}
		}
	}
	if _, err := snapshot.pinned.Seek(0, io.SeekStart); err != nil {
		_ = dst.Close()
		cleanup()
		return err
	}
	hash, size, copyErr := copyLinuxAsset(ctx, dst, snapshot.pinned, linuxAssetLimit(snapshot.role))
	if copyErr == nil && (hash != snapshot.state.SHA256 || size != snapshot.state.Size) {
		copyErr = errors.New("Linux asset changed during preimage capture")
	}
	if copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = syncLinuxAssetDir(dir)
	}
	if copyErr == nil {
		copyErr = checkLinuxAssetSnapshot(ctx, snapshot)
	}
	if copyErr != nil {
		cleanup()
		return fmt.Errorf("capture Linux asset preimage: %w", copyErr)
	}
	return nil
}

func checkPrivateOperationDir(root *os.Root) error {
	info, err := root.Stat(".")
	if err != nil {
		return errors.New("Linux asset operation directory is unavailable")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0700 || !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("Linux asset operation directory is unsafe")
	}
	return nil
}

func copyLinuxAsset(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (string, int64, error) {
	h := sha256.New()
	reader := &contextLinuxAssetReader{ctx: ctx, reader: io.LimitReader(src, limit+1)}
	n, err := io.Copy(io.MultiWriter(dst, h), reader)
	if err != nil {
		return "", n, err
	}
	if n > limit {
		return "", n, errors.New("Linux asset exceeds its fixed size bound")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type contextLinuxAssetReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextLinuxAssetReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func publishLinuxAsset(ctx context.Context, operationID string, snapshot *linuxAssetSnapshot, source *os.File, next linuxAssetState) (linuxAssetState, error) {
	return publishLinuxAssetImpl(ctx, operationID, snapshot, source, next, nil)
}

func publishLinuxAssetRecorded(ctx context.Context, operationID string, snapshot *linuxAssetSnapshot, source *os.File, next linuxAssetState, record func(linuxAssetState) error) (linuxAssetState, error) {
	if record == nil {
		return linuxAssetState{}, errors.New("Linux asset publication requires a durable effect recorder")
	}
	return publishLinuxAssetImpl(ctx, operationID, snapshot, source, next, record)
}

func publishLinuxAssetImpl(ctx context.Context, operationID string, snapshot *linuxAssetSnapshot, source *os.File, next linuxAssetState, record func(linuxAssetState) error) (linuxAssetState, error) {
	if ctx == nil || snapshot == nil || source == nil || !validLinuxAssetOperationID(operationID) || !validLinuxAssetNext(snapshot.role, next) {
		return linuxAssetState{}, errors.New("invalid Linux asset publication arguments")
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		return linuxAssetState{}, err
	}
	if !snapshot.parentComplete {
		return linuxAssetState{}, errors.New("Linux asset parent has not been provisioned")
	}
	name := filepath.Base(snapshot.path)
	pending := name + ".install-" + operationID
	dir, err := snapshot.parent.Open(".")
	if err != nil {
		return linuxAssetState{}, fmt.Errorf("open pinned Linux asset parent: %w", err)
	}
	defer dir.Close()
	dirFD, err := linuxAssetDirFD(dir)
	if err != nil {
		return linuxAssetState{}, err
	}
	fd, err := unix.Openat(dirFD, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return linuxAssetState{}, fmt.Errorf("create anonymous Linux asset stage (O_TMPFILE required): %w", err)
	}
	created := os.NewFile(uintptr(fd), "anonymous-linux-asset-stage")
	if created == nil {
		_ = unix.Close(fd)
		return linuxAssetState{}, errors.New("open anonymous Linux asset stage descriptor")
	}
	defer created.Close()
	activated := false
	var exactStage *linuxAssetState
	cleanup := func() {
		if activated {
			return
		}
		if current, e := snapshot.parent.Lstat(pending); e == nil {
			if held, heldErr := created.Stat(); heldErr == nil && os.SameFile(held, current) {
				if exactStage != nil {
					actual, stagedFile, inspectErr := inspectLinuxAssetPending(ctx, snapshot, pending, held)
					if stagedFile != nil {
						_ = stagedFile.Close()
					}
					if inspectErr != nil || actual != *exactStage {
						return
					}
				}
				_ = snapshot.parent.Remove(pending)
				_ = syncLinuxAssetDir(snapshot.parent)
			}
		}
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		_ = created.Close()
		cleanup()
		return linuxAssetState{}, err
	}
	hash, size, copyErr := copyLinuxAsset(ctx, created, source, linuxAssetLimit(snapshot.role))
	if copyErr == nil && (hash != next.SHA256 || size != next.Size) {
		copyErr = errors.New("Linux asset source digest or size does not match requested publication")
	}
	if copyErr == nil {
		copyErr = created.Chmod(os.FileMode(next.Mode))
	}
	if copyErr == nil {
		copyErr = created.Sync()
	}
	if copyErr == nil {
		_, copyErr = verifyAnonymousLinuxAssetStage(ctx, created, snapshot.role, next, 0)
	}
	if copyErr == nil {
		copyErr = checkLinuxAssetSnapshot(ctx, snapshot)
	}
	if copyErr != nil {
		cleanup()
		return linuxAssetState{}, fmt.Errorf("stage Linux asset publication: %w", copyErr)
	}
	stagedState, err := verifyAnonymousLinuxAssetStage(ctx, created, snapshot.role, next, 0)
	if err != nil {
		return linuxAssetState{}, err
	}
	recordedStage := stagedState
	exactStage = &recordedStage
	if record != nil {
		if err := record(stagedState); err != nil {
			cleanup()
			return linuxAssetState{}, fmt.Errorf("record Linux asset effect: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return linuxAssetState{}, err
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		return linuxAssetState{}, err
	}
	afterCallback, err := verifyAnonymousLinuxAssetStage(ctx, created, snapshot.role, next, 0)
	if err != nil || afterCallback != stagedState {
		return linuxAssetState{}, errors.New("Linux anonymous stage changed during durable effect recording")
	}
	if linuxAssetTestPolicy != nil && linuxAssetTestPolicy.beforeLink != nil {
		if err := linuxAssetTestPolicy.beforeLink(snapshot.role); err != nil {
			return linuxAssetState{}, fmt.Errorf("interrupt before linking Linux asset stage: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return linuxAssetState{}, err
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		return linuxAssetState{}, err
	}
	if _, err := verifyAnonymousLinuxAssetStage(ctx, created, snapshot.role, next, 0); err != nil {
		return linuxAssetState{}, err
	}
	if err := linkAnonymousLinuxAssetStage(fd, dirFD, pending); err != nil {
		return linuxAssetState{}, fmt.Errorf("link anonymous Linux asset stage: %w", err)
	}
	stagedFileInfo, err := created.Stat()
	if err != nil {
		cleanup()
		return linuxAssetState{}, err
	}
	linkedState, stagedFile, err := inspectLinuxAssetPending(ctx, snapshot, pending, stagedFileInfo)
	if err != nil {
		cleanup()
		return linuxAssetState{}, err
	}
	_ = stagedFile.Close()
	syncErr := syncLinuxAssetDir(snapshot.parent)
	if linkedState != afterCallback || syncErr != nil {
		cleanup()
		return linuxAssetState{}, errors.Join(errors.New("linked Linux asset stage failed identity or durability verification"), syncErr)
	}
	if linuxAssetTestPolicy != nil && linuxAssetTestPolicy.beforeRename != nil {
		if err := linuxAssetTestPolicy.beforeRename(snapshot.role); err != nil {
			cleanup()
			return linuxAssetState{}, fmt.Errorf("stage Linux asset publication: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return linuxAssetState{}, err
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		cleanup()
		return linuxAssetState{}, err
	}
	published, err := renameLinuxAsset(ctx, snapshot, pending, name, stagedFileInfo)
	if err != nil {
		activated = published
		if !published {
			cleanup()
			return linuxAssetState{}, err
		}
		actual, actualErr := readLinuxAssetAt(ctx, snapshot.parent, name, mustLinuxAssetSpec(snapshot.role))
		if actualErr != nil {
			return linuxAssetState{}, fmt.Errorf("Linux asset publication outcome is uncertain: %w", errors.Join(err, actualErr))
		}
		return actual, fmt.Errorf("Linux asset publication outcome is uncertain: %w", err)
	}
	activated = true
	actual, actualErr := readLinuxAssetAt(ctx, snapshot.parent, name, mustLinuxAssetSpec(snapshot.role))
	if actualErr == nil {
		publishedInfo, infoErr := snapshot.parent.Lstat(name)
		if infoErr != nil || !os.SameFile(stagedFileInfo, publishedInfo) || actual.SHA256 != next.SHA256 || actual.Size != next.Size || actual.Mode != next.Mode {
			actualErr = errors.New("published Linux asset failed identity or content verification")
		}
	}
	if syncErr := syncLinuxAssetDir(snapshot.parent); actualErr == nil && syncErr != nil {
		actualErr = syncErr
	}
	if actualErr == nil && linuxAssetTestPolicy != nil && linuxAssetTestPolicy.afterRename != nil {
		actualErr = linuxAssetTestPolicy.afterRename(snapshot.role)
	}
	if actualErr != nil {
		return actual, fmt.Errorf("Linux asset publication outcome is uncertain: %w", actualErr)
	}
	return actual, nil
}

func inspectLinuxAssetPending(ctx context.Context, snapshot *linuxAssetSnapshot, pending string, stagedInfo os.FileInfo) (linuxAssetState, *os.File, error) {
	info, err := snapshot.parent.Lstat(pending)
	if err != nil {
		return linuxAssetState{}, nil, err
	}
	if stagedInfo == nil || !os.SameFile(stagedInfo, info) {
		return linuxAssetState{}, nil, errors.New("Linux asset pending inode changed")
	}
	file, state, err := inspectLinuxAssetFile(ctx, snapshot.parent, pending, info, mustLinuxAssetSpec(snapshot.role))
	if err != nil {
		return linuxAssetState{}, nil, err
	}
	return state, file, nil
}

// verifyAnonymousLinuxAssetStage validates bytes and ownership through the
// retained O_TMPFILE descriptor. nlink is zero before linkat and one after.
func verifyAnonymousLinuxAssetStage(ctx context.Context, file *os.File, role linuxAssetRole, expected linuxAssetState, nlink uint64) (linuxAssetState, error) {
	var zero linuxAssetState
	if ctx == nil || file == nil || !validLinuxAssetNext(role, linuxAssetState{Present: expected.Present, SHA256: expected.SHA256, Size: expected.Size, Mode: expected.Mode}) {
		return zero, errors.New("invalid anonymous Linux asset stage verification arguments")
	}
	info, err := file.Stat()
	if err != nil {
		return zero, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(expected.Mode) || stat.Uid != linuxAssetExpectedUID() || stat.Nlink != nlink || info.Size() != expected.Size || stat.Dev == 0 || stat.Ino == 0 {
		return zero, errors.New("anonymous Linux asset stage descriptor has unexpected identity or metadata")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	hash, size, err := hashLinuxAsset(ctx, file, linuxAssetLimit(role))
	if err != nil {
		return zero, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return zero, err
	}
	after, err := file.Stat()
	if err != nil {
		return zero, err
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(info, after) || after.Size() != info.Size() || after.Mode().Perm() != info.Mode().Perm() || afterStat.Uid != stat.Uid || afterStat.Nlink != stat.Nlink || afterStat.Dev != stat.Dev || afterStat.Ino != stat.Ino {
		return zero, errors.New("anonymous Linux asset stage metadata changed while hashing")
	}
	if hash != expected.SHA256 || size != expected.Size {
		return zero, errors.New("anonymous Linux asset stage bytes changed")
	}
	return linuxAssetState{Present: true, SHA256: hash, Size: size, Mode: uint32(info.Mode().Perm()), Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func linkAnonymousLinuxAssetStage(fileFD, parentFD int, name string) error {
	err := unix.Linkat(fileFD, "", parentFD, name, unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EPERM) && !errors.Is(err, unix.ENOENT) {
		return err
	}
	// Some kernels permit O_TMPFILE but restrict AT_EMPTY_PATH. The fallback
	// still names only the procfd symlink for this held descriptor.
	procFD := fmt.Sprintf("/proc/self/fd/%d", fileFD)
	return unix.Linkat(unix.AT_FDCWD, procFD, parentFD, name, unix.AT_SYMLINK_FOLLOW)
}

func validLinuxAssetOperationID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validLinuxAssetNext(role linuxAssetRole, state linuxAssetState) bool {
	spec, ok := linuxAssetSpecForRole(role)
	if !ok || !state.Present || state.Size < 0 || state.Size > linuxAssetLimit(role) || state.Mode != spec.Mode || state.Device != 0 || state.Inode != 0 || len(state.SHA256) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(state.SHA256)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(state.SHA256) == state.SHA256
}

func renameLinuxAsset(ctx context.Context, snapshot *linuxAssetSnapshot, pending, name string, staged os.FileInfo) (bool, error) {
	dir, err := snapshot.parent.Open(".")
	if err != nil {
		return false, fmt.Errorf("open Linux asset parent for publication: %w", err)
	}
	defer dir.Close()
	raw, err := dir.SyscallConn()
	if err != nil {
		return false, err
	}
	var renameErr error
	controlErr := raw.Control(func(fd uintptr) {
		flag := uint(0)
		if snapshot.state.Present {
			flag = unix.RENAME_EXCHANGE
		} else {
			flag = unix.RENAME_NOREPLACE
		}
		renameErr = unix.Renameat2(int(fd), pending, int(fd), name, flag)
	})
	if controlErr != nil {
		return false, controlErr
	}
	if renameErr != nil {
		return false, fmt.Errorf("atomically publish Linux asset: %w", renameErr)
	}
	activated := true
	if snapshot.state.Present {
		old, oldErr := snapshot.parent.Lstat(pending)
		pinned, pinnedErr := snapshot.pinned.Stat()
		oldState, stateErr := readLinuxAssetAt(ctx, snapshot.parent, pending, mustLinuxAssetSpec(snapshot.role))
		if oldErr != nil || pinnedErr != nil || stateErr != nil || oldState != snapshot.state || !os.SameFile(old, pinned) {
			// The target changed after the last check. Restore its namespace only
			// while the candidate still occupies the public name; retain ambiguity.
			current, currentErr := snapshot.parent.Lstat(name)
			if currentErr == nil && staged != nil && os.SameFile(current, staged) {
				fd, fdErr := linuxAssetDirFD(dir)
				if fdErr != nil {
					return true, errors.New("asset target changed and exchange rollback descriptor is unavailable")
				}
				if reverseErr := unix.Renameat2(fd, pending, fd, name, unix.RENAME_EXCHANGE); reverseErr != nil {
					return true, fmt.Errorf("asset target changed and exchange rollback is uncertain: %w", reverseErr)
				}
				activated = false
			}
			return activated, errors.New("Linux asset target changed during atomic replacement")
		}
		if err := snapshot.parent.Remove(pending); err != nil {
			return true, fmt.Errorf("remove exchanged Linux asset preimage: %w", err)
		}
	}
	return activated, nil
}

func linuxAssetDirFD(file *os.File) (int, error) {
	fd := -1
	raw, err := file.SyscallConn()
	if err != nil {
		return -1, err
	}
	err = raw.Control(func(value uintptr) { fd = int(value) })
	if err != nil {
		return -1, err
	}
	if fd < 0 {
		return -1, errors.New("invalid Linux asset directory descriptor")
	}
	return fd, nil
}

func readLinuxAssetAt(ctx context.Context, root *os.Root, name string, spec linuxAssetSpec) (linuxAssetState, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return linuxAssetState{}, err
	}
	file, state, err := inspectLinuxAssetFile(ctx, root, name, info, spec)
	if file != nil {
		_ = file.Close()
	}
	return state, err
}

func removeLinuxAsset(ctx context.Context, snapshot *linuxAssetSnapshot) error {
	if ctx == nil || snapshot == nil {
		return errors.New("invalid Linux asset removal arguments")
	}
	state, err := currentLinuxAsset(ctx, snapshot)
	if err != nil {
		return err
	}
	if !state.Present {
		return nil
	}
	if state != snapshot.state {
		return errors.New("Linux asset no longer matches captured state")
	}
	if err := checkLinuxAssetSnapshot(ctx, snapshot); err != nil {
		return err
	}
	name := filepath.Base(snapshot.path)
	quarantine := fmt.Sprintf("%s.remove-%x-%x", name, snapshot.state.Device, snapshot.state.Inode)
	dir, err := snapshot.parent.Open(".")
	if err != nil {
		return fmt.Errorf("open Linux asset parent for removal: %w", err)
	}
	defer dir.Close()
	fd, err := linuxAssetDirFD(dir)
	if err != nil {
		return err
	}
	if err := unix.Renameat2(fd, name, fd, quarantine, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("pin Linux asset before removal: %w", err)
	}
	restore := func() error {
		return unix.Renameat2(fd, quarantine, fd, name, unix.RENAME_NOREPLACE)
	}
	if err := ctx.Err(); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("cancelled Linux asset removal has uncertain restore: %w", restoreErr)
		}
		return err
	}
	quarantined, stateErr := readLinuxAssetAt(ctx, snapshot.parent, quarantine, mustLinuxAssetSpec(snapshot.role))
	quarantineInfo, infoErr := snapshot.parent.Lstat(quarantine)
	pinned, pinnedErr := snapshot.pinned.Stat()
	if stateErr != nil || infoErr != nil || pinnedErr != nil || quarantined != snapshot.state || !os.SameFile(quarantineInfo, pinned) {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("Linux asset changed during removal and restore is uncertain: %w", restoreErr)
		}
		return errors.New("Linux asset changed during removal; original target restored")
	}
	if err := snapshot.parent.Remove(quarantine); err != nil {
		return fmt.Errorf("remove captured Linux asset: %w", err)
	}
	if err := syncLinuxAssetDir(snapshot.parent); err != nil {
		return fmt.Errorf("sync Linux asset parent after removal: %w", err)
	}
	return ctx.Err()
}

func syncLinuxAssetDir(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
