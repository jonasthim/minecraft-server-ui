package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

const activationPendingName = "activation.pending.json"

type activationBlock struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
}

// activationRoot binds the fixed protected .manager directory without accepting
// links. The marker is only a launch blocker; memory authority stays in the
// committed instance/installation records and verified sealed specification.
func activationRoot(ctx context.Context, dataDir, id string, create bool) (*os.Root, error) {
	paths, err := Paths(dataDir, id)
	if err != nil {
		return nil, err
	}
	server, err := safepath.OpenRoot(ctx, paths.Server)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	info, err := server.Lstat(".manager")
	if os.IsNotExist(err) && create {
		if err = server.Mkdir(".manager", 0700); err != nil {
			return nil, err
		}
		if err = syncActivationDir(server); err != nil {
			return nil, err
		}
		info, err = server.Lstat(".manager")
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("unsafe protected activation directory")
	}
	root, err := safepath.OpenRoot(ctx, filepath.Join(paths.Server, ".manager"))
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("protected activation directory changed")
	}
	return root, nil
}
func syncActivationDir(root *os.Root) error {
	if runtime.GOOS == "windows" {
		return nil
	} // native power-loss durability remains unqualified
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func readActivationBlock(root *os.Root, id string) (bool, error) {
	info, err := root.Lstat(activationPendingName)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return false, fmt.Errorf("invalid activation blocker")
	}
	file, err := root.Open(activationPendingName)
	if err != nil {
		return false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false, fmt.Errorf("activation blocker changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return false, err
	}
	expected, err := json.Marshal(activationBlock{Version: 1, InstanceID: id})
	if err != nil || !bytes.Equal(data, expected) {
		return false, fmt.Errorf("invalid activation blocker identity or encoding")
	}

	return true, nil
}

func MarkActivationPending(ctx context.Context, dataDir, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := activationRoot(ctx, dataDir, id, true)
	if err != nil {
		return err
	}
	defer root.Close()
	pending, err := readActivationBlock(root, id)
	if err != nil {
		return err
	}
	if pending {
		return nil
	}
	bytes, err := json.Marshal(activationBlock{Version: 1, InstanceID: id})
	if err != nil {
		return err
	}
	// The fixed temporary name is inside the protected directory and never followed.
	temp := activationPendingName + ".tmp"
	if info, statErr := root.Lstat(temp); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsafe pending activation staging")
		}
		if err = root.Remove(temp); err != nil {
			return err
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(bytes)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(temp, activationPendingName); err != nil {
		return err
	}
	return syncActivationDir(root)
}

// CheckActivationReady is enforced by Native.Start and independently by Run
// after acquiring launcher admission, including systemd boot before the manager.
func CheckActivationReady(ctx context.Context, dataDir, id string) error {
	if err := checkContentTransactionReady(ctx, dataDir, id); err != nil {
		return domain.Wrap(domain.CodeUnavailable, "content transaction pending or unavailable; manager recovery required", err)
	}
	if err := checkBackupRestoreReady(ctx, dataDir, id); err != nil {
		return domain.Wrap(domain.CodeUnavailable, "backup restore pending or unavailable; manager recovery required", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := activationRoot(ctx, dataDir, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return domain.Wrap(domain.CodeUnavailable, "configuration activation pending or unavailable; manager recovery required", err)
	}
	defer root.Close()
	pending, err := readActivationBlock(root, id)
	if err != nil || pending {
		return domain.Wrap(domain.CodeUnavailable, "configuration activation pending; manager recovery required", err)
	}
	return nil
}

// ClearActivationPending requires the caller's stopped boundary and successful
// config recovery plus verified DB-committed launch. Malformed state is retained.
func ClearActivationPending(ctx context.Context, dataDir, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := activationRoot(ctx, dataDir, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	pending, err := readActivationBlock(root, id)
	if err != nil || !pending {
		return err
	}
	if err = root.Remove(activationPendingName); err != nil {
		return err
	}
	return syncActivationDir(root)
}

// PendingConfig reports an instance-owned config journal without following it.
// Recovery cannot clear the boot guard until this journal has been resolved.
func PendingConfig(dataDir, id string) (bool, error) {
	paths, err := Paths(dataDir, id)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(paths.State, "config.pending.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("unsafe pending configuration journal")
	}
	return true, nil
}
