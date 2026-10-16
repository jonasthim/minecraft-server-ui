package instance

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const configIntentPath = "state/config.pending.json"

type fileSnapshot struct {
	Exists bool   `json:"exists"`
	Data   []byte `json:"data"`
}
type managedSnapshot struct {
	Properties fileSnapshot `json:"properties"`
	EULA       fileSnapshot `json:"eula"`
}
type configIntent struct {
	OwnerToken      string          `json:"owner_token"`
	BeforeUpdatedAt time.Time       `json:"before_updated_at"`
	AfterUpdatedAt  time.Time       `json:"after_updated_at"`
	Before          managedSnapshot `json:"before"`
}

func snapshotFile(root *os.Root, path string) (fileSnapshot, error) {
	return snapshotFileLimit(root, path, 1<<20)
}
func snapshotFileLimit(root *os.Root, path string, limit int64) (fileSnapshot, error) {
	info, err := root.Lstat(path)
	if os.IsNotExist(err) {
		return fileSnapshot{}, nil
	}
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, domain.E(domain.CodeConflict, "managed configuration path is not a regular file")
	}
	f, err := root.Open(path)
	if err != nil {
		return fileSnapshot{}, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return fileSnapshot{}, err
	}
	if int64(len(raw)) > limit {
		return fileSnapshot{}, domain.Ef(domain.CodeValidationFailed, "managed configuration file exceeds %d bytes", limit)
	}
	return fileSnapshot{true, raw}, nil
}
func snapshotManaged(root *os.Root) (managedSnapshot, error) {
	p, err := snapshotFile(root, "server/server.properties")
	if err != nil {
		return managedSnapshot{}, err
	}
	e, err := snapshotFile(root, "server/eula.txt")
	if err != nil {
		return managedSnapshot{}, err
	}
	return managedSnapshot{p, e}, nil
}
func persistConfigIntent(ctx context.Context, root *os.Root, intent configIntent) error {
	raw, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	return atomicFile(ctx, root, configIntentPath, raw)
}
func clearConfigIntent(root *os.Root) error {
	if err := root.Remove(configIntentPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(root, "state")
}
func restoreSnapshot(ctx context.Context, root *os.Root, snapshot managedSnapshot) error {
	restore := func(path string, old fileSnapshot) error {
		if old.Exists {
			return atomicFile(ctx, root, path, old.Data)
		}
		info, err := root.Lstat(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return domain.E(domain.CodeConflict, "refusing to remove unexpected managed configuration path")
		}
		return root.Remove(path)
	}
	if err := errors.Join(restore("server/server.properties", snapshot.Properties), restore("server/eula.txt", snapshot.EULA)); err != nil {
		return err
	}
	return syncDirectory(root, "server")
}
func rollbackConfig(ctx context.Context, root *os.Root, snapshot managedSnapshot) error {
	if err := restoreSnapshot(ctx, root, snapshot); err != nil {
		return err
	}
	return clearConfigIntent(root)
}

// A pending intent authenticates an exact before-file snapshot and both DB
// revisions. Uncommitted proposed bytes are never re-imported as unknown extras.
// File-only changes made after the snapshot during this interrupted transaction
// cannot be distinguished from proposed bytes and are intentionally rolled back.
func (s *Service) recoverConfig(ctx context.Context, root *os.Root, d Detail) error {
	snapshot, err := snapshotFileLimit(root, configIntentPath, 4<<20)
	if err != nil {
		return err
	}
	if !snapshot.Exists {
		return nil
	}
	var intent configIntent
	if err = json.Unmarshal(snapshot.Data, &intent); err != nil {
		return domain.Wrap(domain.CodeConflict, "invalid config recovery journal", err)
	}
	token, err := s.ownerToken(ctx, d.Instance.ID)
	if err != nil {
		return err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(intent.OwnerToken)) != 1 {
		return domain.E(domain.CodeConflict, "config recovery ownership does not match")
	}
	if intent.BeforeUpdatedAt.IsZero() || intent.AfterUpdatedAt.IsZero() || intent.BeforeUpdatedAt.Equal(intent.AfterUpdatedAt) {
		return domain.E(domain.CodeConflict, "invalid config recovery revisions")
	}
	for _, file := range []fileSnapshot{intent.Before.Properties, intent.Before.EULA} {
		if len(file.Data) > 1<<20 {
			return domain.E(domain.CodeConflict, "config recovery snapshot exceeds limits")
		}
	}
	switch {
	case d.Instance.UpdatedAt.Equal(intent.AfterUpdatedAt):
		return clearConfigIntent(root)
	case d.Instance.UpdatedAt.Equal(intent.BeforeUpdatedAt):
		if err = restoreSnapshot(ctx, root, intent.Before); err != nil {
			return fmt.Errorf("restore interrupted configuration: %w", err)
		}
		return clearConfigIntent(root)
	default:
		return domain.E(domain.CodeConflict, "config recovery revision does not match the instance")
	}
}
func mergeFileOnlyExtras(config *domain.InstanceConfig, file map[string]string, known map[string]string) {
	cloned := make(map[string]string, len(config.Extra)+len(file))
	for key, value := range config.Extra {
		cloned[key] = value
	}
	for key, value := range file {
		if _, wasKnown := known[key]; !wasKnown {
			if _, present := cloned[key]; !present {
				cloned[key] = value
			}
		}
	}
	config.Extra = cloned
}
