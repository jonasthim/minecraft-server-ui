package instance

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type ReconcileDeferred struct {
	InstanceID string
	Code       domain.ErrorCode
	Reason     string
}
type ReconcileResult struct {
	Recovered []string
	Deferred  []ReconcileDeferred
}

// Reconcile runs after jobs startup, before HTTP or autostart. Surviving live or
// unverifiable instances are protected and reported as deferred. It only restores
// a staged directory that carries the durable intent's exact ownership token.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	result := ReconcileResult{Recovered: []string{}, Deferred: []ReconcileDeferred{}}
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id FROM instance_mutations ORDER BY instance_id`)
	if err != nil {
		return result, fmt.Errorf("list instance journals: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return result, err
		}
		ids = append(ids, id)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return result, err
	}
	deferred := map[string]bool{}
	handle := func(id string, operation func() error) error {
		err := s.mutate(ctx, id, operation)
		if err == nil {
			result.Recovered = append(result.Recovered, id)
			return nil
		}
		de := domain.AsError(err)
		switch de.Code {
		case domain.CodeInstanceRunning, domain.CodeInstanceBusy, domain.CodeUnavailable:
			result.Deferred = append(result.Deferred, ReconcileDeferred{id, de.Code, de.Message})
			deferred[id] = true
			return nil
		default:
			return fmt.Errorf("reconcile instance %s: %w", id, err)
		}
	}
	for _, id := range ids {
		if err = handle(id, func() error { return s.recoverDeletion(ctx, id) }); err != nil {
			return result, err
		}
	}
	instances, err := s.List(ctx)
	if err != nil {
		return result, err
	}
	for _, d := range instances {
		if deferred[d.Instance.ID] {
			continue
		}
		id := d.Instance.ID
		if err = handle(id, func() error { return s.materialize(ctx, id) }); err != nil {
			return result, err
		}
	}
	return result, nil
}

// Materialize must finish under stopped+idle exclusion before a future start.
// It cannot be called from a job already holding this instance's execution slot:
// provider orchestration needs a reviewed current-job admission contract instead.
func (s *Service) Materialize(ctx context.Context, id string) error {
	return s.mutate(ctx, id, func() error {
		if err := s.recoverDeletion(ctx, id); err != nil {
			return err
		}
		return s.materialize(ctx, id)
	})
}
func (s *Service) materialize(ctx context.Context, id string) error {
	d, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if err = ValidateConfig(d.Config); err != nil {
		return err
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dirs.Root)
	if err != nil {
		return fmt.Errorf("open owned instance for materialization: %w", err)
	}
	defer root.Close()
	if err = s.verifyOwner(ctx, id, root); err != nil {
		return err
	}
	if err = s.recoverConfig(ctx, root, d); err != nil {
		return err
	}
	extras, err := unknownProperties(root)
	if err != nil {
		return err
	}
	mergeFileOnlyExtras(&d.Config, extras, d.Config.Extra)
	if err = ValidateConfig(d.Config); err != nil {
		return err
	}
	management, err := s.optionalManagement(ctx, id)
	if err != nil {
		return err
	}
	return writeManaged(ctx, root, d.Instance, d.Config, d.EULA, management)
}
func (s *Service) ownerToken(ctx context.Context, id string) (string, error) {
	var token string
	if err := s.db.QueryRowContext(ctx, `SELECT ownership_token FROM instance_settings WHERE instance_id=?`, id).Scan(&token); err != nil {
		return "", fmt.Errorf("read instance ownership: %w", err)
	}
	if token == "" {
		return "", domain.E(domain.CodeConflict, "instance ownership is unestablished; refusing to adopt its directory")
	}
	return token, nil
}
func (s *Service) verifyOwner(ctx context.Context, id string, root *os.Root) error {
	token, err := s.ownerToken(ctx, id)
	if err != nil {
		return err
	}
	return verifyToken(root, token)
}
func verifyToken(root *os.Root, token string) error {
	if token == "" {
		return domain.E(domain.CodeConflict, "instance ownership token is missing")
	}
	f, err := root.Open("state/instance-owner")
	if err != nil {
		return domain.Wrap(domain.CodeConflict, "instance ownership marker cannot be verified", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1024))
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(raw, []byte(token)) != 1 {
		return domain.E(domain.CodeConflict, "instance directory ownership does not match its journal")
	}
	return nil
}
func (s *Service) recoverDeletion(ctx context.Context, id string) error {
	var kind, staged, token string
	err := s.db.QueryRowContext(ctx, `SELECT kind,staged_name,ownership_token FROM instance_mutations WHERE instance_id=?`, id).Scan(&kind, &staged, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read instance journal: %w", err)
	}
	if kind != "delete" || token == "" || !strings.HasPrefix(staged, ".deleting-"+id+"-") || strings.ContainsAny(staged, "/\\") || portablePart(staged) != nil {
		return domain.E(domain.CodeConflict, "invalid instance deletion journal")
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(dirs.Root))
	if err != nil {
		return err
	}
	defer parent.Close()
	existsOwned := func(name string) (bool, error) {
		info, err := parent.Lstat(name)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, domain.E(domain.CodeConflict, "journal path is not an owned directory")
		}
		root, err := parent.OpenRoot(name)
		if err != nil {
			return false, err
		}
		defer root.Close()
		if err = verifyToken(root, token); err != nil {
			return false, err
		}
		return true, nil
	}
	original, err := existsOwned(id)
	if err != nil {
		return err
	}
	proofPresent, err := cleanupProof(parent, staged, token)
	if err != nil {
		return err
	}
	stagedExists := false
	if proofPresent {
		info, statErr := parent.Lstat(staged)
		if statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return domain.E(domain.CodeConflict, "cleanup journal path is not a directory")
			}
			stagedExists = true
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
	} else {
		stagedExists, err = existsOwned(staged)
	}
	if err != nil {
		return err
	}
	var row int
	err = s.db.QueryRowContext(ctx, `SELECT 1 FROM instances WHERE id=?`, id).Scan(&row)
	present := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if present {
		if proofPresent {
			return domain.E(domain.CodeConflict, "cleanup proof exists for an undeleted instance")
		}
		current, err := s.ownerToken(ctx, id)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare([]byte(current), []byte(token)) != 1 {
			return domain.E(domain.CodeConflict, "instance id was reused while deletion remained journaled")
		}
		if original && stagedExists {
			return domain.E(domain.CodeConflict, "both original and staged instance directories exist")
		}
		if !original && !stagedExists {
			return domain.E(domain.CodeConflict, "owned instance directory is missing; refusing to recreate world data")
		}
		if stagedExists {
			if err = parent.Rename(staged, id); err != nil {
				return fmt.Errorf("restore interrupted deletion: %w", err)
			}
			if err = syncDirectory(parent, "."); err != nil {
				return err
			}
		}
	} else {
		if original {
			return domain.E(domain.CodeConflict, "deleted instance has an unexpected original directory")
		}
		if stagedExists {
			if err = cleanupOwnedDeletion(ctx, parent, staged, token); err != nil {
				return fmt.Errorf("finish interrupted deletion: %w", err)
			}
		}
	}
	if proofPresent && !stagedExists {
		if err = syncDirectory(parent, "."); err != nil {
			return err
		}
		if err = parent.Remove(staged + ".owner"); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM instance_mutations WHERE instance_id=?`, id); err != nil {
		return fmt.Errorf("clear recovered instance journal: %w", err)
	}
	return nil
}

// The authenticated sibling proof survives deletion of any subtree, including
// state/instance-owner. A failed RemoveAll or process interruption can therefore
// resume without weakening ownership validation for unrelated directories.
func cleanupProof(parent *os.Root, staged, token string) (bool, error) {
	name := staged + ".owner"
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, domain.E(domain.CodeConflict, "cleanup proof is not a regular file")
	}
	f, err := parent.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 1024))
	if err != nil {
		return false, err
	}
	if token == "" || subtle.ConstantTimeCompare(raw, []byte(token)) != 1 {
		return false, domain.E(domain.CodeConflict, "cleanup ownership proof does not match its durable journal")
	}
	return true, nil
}
func cleanupOwnedDeletion(ctx context.Context, parent *os.Root, staged, token string) error {
	proof, err := cleanupProof(parent, staged, token)
	if err != nil {
		return err
	}
	if !proof {
		root, err := parent.OpenRoot(staged)
		if err != nil {
			return err
		}
		err = verifyToken(root, token)
		root.Close()
		if err != nil {
			return err
		}
		if err = atomicFile(ctx, parent, staged+".owner", []byte(token)); err != nil {
			return fmt.Errorf("persist cleanup ownership proof: %w", err)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = parent.RemoveAll(staged); err != nil {
		return err
	}
	if err = syncDirectory(parent, "."); err != nil {
		return err
	}
	if err = parent.Remove(staged + ".owner"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(parent, ".")
}
