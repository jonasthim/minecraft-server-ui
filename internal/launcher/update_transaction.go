package launcher

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
)

// checkUpdateTransactionReady does not consult SQLite. Independent launch and
// boot must fail closed while an update needs recovery or the current world's
// compatibility with a restored executable generation remains unresolved.
func checkUpdateTransactionReady(ctx context.Context, dataDir, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := Paths(dataDir, id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open update blocker directory: %w", err)
	}
	defer root.Close()
	for _, name := range []string{"update-transaction.json", "update-world-risk.json"} {
		_, err = root.Lstat(name)
		if err == nil {
			return fmt.Errorf("instance update requires manager reconciliation")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect update blocker: %w", err)
		}
	}
	return ctx.Err()
}

// UpdateBlocker is an exact coordinator identity independent of SQLite. Presence
// blocks launch even when malformed or the matching SQL row has been lost.
type UpdateBlocker struct {
	Version       int    `json:"version"`
	InstanceID    string `json:"instance_id"`
	TransactionID string `json:"transaction_id"`
	JobID         string `json:"job_id"`
	Kind          string `json:"kind"`
}

func validUpdateBlocker(marker UpdateBlocker) bool {
	_, txErr := hex.DecodeString(marker.TransactionID)
	_, jobErr := hex.DecodeString(marker.JobID)
	return marker.Version == 1 && domain.ValidateInstanceID(marker.InstanceID) == nil && len(marker.TransactionID) == 32 && txErr == nil && marker.TransactionID == strings.ToLower(marker.TransactionID) && len(marker.JobID) == 32 && jobErr == nil && marker.JobID == strings.ToLower(marker.JobID) && (marker.Kind == "update" || marker.Kind == "instance_rollback")
}
func ReadUpdateBlocker(ctx context.Context, dataDir, id string) (UpdateBlocker, bool, error) {
	return readUpdateBlockerName(ctx, dataDir, id, "update-transaction.json")
}
func ReadWorldRiskBlocker(ctx context.Context, dataDir, id string) (UpdateBlocker, bool, error) {
	return readUpdateBlockerName(ctx, dataDir, id, "update-world-risk.json")
}
func readUpdateBlockerName(ctx context.Context, dataDir, id, name string) (UpdateBlocker, bool, error) {
	paths, err := Paths(dataDir, id)
	if err != nil {
		return UpdateBlocker{}, false, err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if errors.Is(err, fs.ErrNotExist) {
		return UpdateBlocker{}, false, nil
	}
	if err != nil {
		return UpdateBlocker{}, false, err
	}
	defer root.Close()
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return UpdateBlocker{}, false, nil
	}
	if err != nil {
		return UpdateBlocker{}, true, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return UpdateBlocker{}, true, fmt.Errorf("unsafe update blocker")
	}
	file, err := root.Open(name)
	if err != nil {
		return UpdateBlocker{}, true, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return UpdateBlocker{}, true, fmt.Errorf("update blocker changed")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1025))
	if err != nil {
		return UpdateBlocker{}, true, err
	}
	var marker UpdateBlocker
	if json.Unmarshal(raw, &marker) != nil || !validUpdateBlocker(marker) || marker.InstanceID != id {
		return marker, true, fmt.Errorf("update blocker identity invalid")
	}
	canonical, _ := json.Marshal(marker)
	if !bytes.Equal(raw, canonical) {
		return marker, true, fmt.Errorf("update blocker encoding invalid")
	}
	return marker, true, nil
}
func WriteUpdateBlocker(ctx context.Context, dataDir string, marker UpdateBlocker) error {
	return writeUpdateBlockerName(ctx, dataDir, marker, "update-transaction.json")
}
func WriteWorldRiskBlocker(ctx context.Context, dataDir string, marker UpdateBlocker) error {
	return writeUpdateBlockerName(ctx, dataDir, marker, "update-world-risk.json")
}
func writeUpdateBlockerName(ctx context.Context, dataDir string, marker UpdateBlocker, name string) error {
	if !validUpdateBlocker(marker) {
		return fmt.Errorf("invalid update blocker identity")
	}
	current, present, err := readUpdateBlockerName(ctx, dataDir, marker.InstanceID, name)
	if err != nil {
		return err
	}
	if present {
		if current != marker {
			return fmt.Errorf("foreign update blocker")
		}
		return nil
	}
	paths, err := Paths(dataDir, marker.InstanceID)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, _ := json.Marshal(marker)
	temp := name + ".tmp"
	if _, err = root.Lstat(temp); err == nil {
		return fmt.Errorf("update blocker temporary file needs recovery")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(raw)
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
	if _, err = root.Lstat(name); err == nil {
		return fmt.Errorf("concurrent update blocker appeared")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err = root.Rename(temp, name); err != nil {
		return err
	}
	return syncActivationDir(root)
}
func ClearUpdateBlocker(ctx context.Context, dataDir string, expected UpdateBlocker) error {
	return clearUpdateBlockerName(ctx, dataDir, expected, "update-transaction.json")
}
func ClearWorldRiskBlocker(ctx context.Context, dataDir string, expected UpdateBlocker) error {
	return clearUpdateBlockerName(ctx, dataDir, expected, "update-world-risk.json")
}
func clearUpdateBlockerName(ctx context.Context, dataDir string, expected UpdateBlocker, name string) error {
	actual, present, err := readUpdateBlockerName(ctx, dataDir, expected.InstanceID, name)
	if err != nil || !present {
		return err
	}
	if actual != expected {
		return fmt.Errorf("foreign update blocker")
	}
	paths, err := Paths(dataDir, expected.InstanceID)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(name); err != nil {
		return err
	}
	return syncActivationDir(root)
}

// TransferUpdateBlocker publishes an already-durable consented handoff under
// stopped admission. The coordinator SQL contains reciprocal parent/successor
// links before this call; either exact marker is recoverable after interruption.
// The independent world-risk blocker is deliberately untouched.
func TransferUpdateBlocker(ctx context.Context, dataDir string, previous, next UpdateBlocker) error {
	if !validUpdateBlocker(previous) || !validUpdateBlocker(next) || previous.InstanceID != next.InstanceID || previous.Kind != "update" || next.Kind != "instance_rollback" {
		return fmt.Errorf("invalid update blocker handoff")
	}
	actual, present, err := ReadUpdateBlocker(ctx, dataDir, previous.InstanceID)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("handoff update blocker missing")
	}
	if actual == next {
		return nil
	}
	if actual != previous {
		return fmt.Errorf("foreign update blocker handoff")
	}
	paths, err := Paths(dataDir, previous.InstanceID)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, paths.State)
	if err != nil {
		return err
	}
	defer root.Close()
	raw, _ := json.Marshal(next)
	const temp = "update-handoff.tmp"
	if info, statErr := root.Lstat(temp); statErr == nil {
		if !info.Mode().IsRegular() || info.Size() > 1024 {
			return fmt.Errorf("unsafe update handoff temporary file")
		}
		file, err := root.Open(temp)
		if err != nil {
			return err
		}
		staged, readErr := io.ReadAll(io.LimitReader(file, 1025))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !bytes.Equal(staged, raw) {
			return fmt.Errorf("foreign update handoff temporary file")
		}
		if err = root.Remove(temp); err != nil {
			return err
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return statErr
	}
	file, err := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	_, err = file.Write(raw)
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
	actual, present, err = ReadUpdateBlocker(ctx, dataDir, previous.InstanceID)
	if err != nil {
		return err
	}
	if !present || actual != previous {
		return fmt.Errorf("update handoff preimage changed")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(temp, "update-transaction.json"); err != nil {
		return err
	}
	if err = syncActivationDir(root); err != nil {
		return err
	}
	actual, present, err = ReadUpdateBlocker(ctx, dataDir, next.InstanceID)
	if err != nil {
		return err
	}
	if !present || actual != next {
		return fmt.Errorf("update handoff publication changed")
	}
	return nil
}

func UpdateActivationPending(ctx context.Context, dataDir, id string) (bool, error) {
	root, err := activationRoot(ctx, dataDir, id, false)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	return readActivationBlock(root, id)
}
