package world

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"regexp"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

var ownedExportName = regexp.MustCompile(`^[0-9a-f]{32}\.zip$`)

// ReconcileExports removes crash-interrupted staging files before HTTP starts.
// It refuses globally active captures/transfers and excludes new admission until
// completion. Call only at startup while the manager lock is held and queue
// dispatch is paused. Queued jobs are preserved and do not prevent cleanup.
// Only persisted instance directories and exact private staging names
// are considered; unknown files, directories, links and special files survive.
// A partial ZIP is eligible because a crash can interrupt its first write.
func (s *Service) ReconcileExports(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.db == nil {
		return domain.E(domain.CodeUnavailable, "world export reconciliation is not configured")
	}
	if !exportAdmission.TryLock() {
		return domain.E(domain.CodeInstanceBusy, "world export admission is active; retry reconciliation")
	}
	defer exportAdmission.Unlock()
	if len(exportSlots) != 0 {
		return domain.E(domain.CodeInstanceBusy, "world exports are active; reconciliation refused")
	}
	ctx, cancel := context.WithTimeout(ctx, s.limits.discover)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM instances ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list instances for world export reconciliation: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if len(ids) >= s.limits.entries {
			rows.Close()
			return bound()
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	remaining := s.limits.entries
	for _, id := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = s.reconcileInstanceExports(ctx, id, &remaining); err != nil {
			return fmt.Errorf("reconcile world export staging for instance %s: %w", id, err)
		}
	}
	return nil
}

func (s *Service) reconcileInstanceExports(ctx context.Context, id string, remaining *int) error {
	inst, err := s.openInstance(id)
	if err != nil {
		return err
	}
	defer inst.Close()
	backups, err := openChild(inst, "backups")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer backups.Close()
	staging, err := openChild(backups, ".exports")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer staging.Close()
	dir, err := openRead(staging, ".", true)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			*remaining--
			if *remaining < 0 {
				return bound()
			}
			name := entry.Name()
			if !ownedExportName.MatchString(name) {
				continue
			}
			before, err := staging.Lstat(name)
			if err != nil {
				return err
			}
			if !before.Mode().IsRegular() {
				continue
			}
			f, err := openRead(staging, name, false)
			if err != nil {
				// The platform opener rejects hard links and reparse points.
				if domain.AsError(err).Code == domain.CodeValidationFailed {
					continue
				}
				return err
			}
			opened, statErr := f.Stat()
			current, currentErr := staging.Lstat(name)
			closeErr := f.Close()
			if statErr != nil || currentErr != nil || !sameEntry(before, opened) || !sameEntry(before, current) || !current.Mode().IsRegular() {
				return changed()
			}
			if closeErr != nil {
				return closeErr
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = staging.Remove(name); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
