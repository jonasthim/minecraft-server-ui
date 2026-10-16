package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"os"
	"time"
)

// ReconcileLeases must run before other recovery, HTTP, autostart and Resume.
// It attempts every outstanding lease even if one repair fails. Dead/replaced
// JVMs are distinct outcomes; they never receive a save-on for a recycled PID.
func (s *Service) ReconcileLeases(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,instance_id,identity_json,expires_at FROM backup_leases WHERE state IN ('armed','unresolved') ORDER BY expires_at,id`)
	if err != nil {
		return err
	}
	type entry struct{ id, instance, raw, expiry string }
	entries := []entry{}
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.instance, &e.raw, &e.expiry); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	var all error
	for _, e := range entries {
		var identity domain.ProcessIdentity
		expiry, parseErr := time.Parse(time.RFC3339Nano, e.expiry)
		if json.Unmarshal([]byte(e.raw), &identity) != nil || identity.InstanceID != e.instance || !validKey(e.id) || parseErr != nil {
			all = errors.Join(all, errCorrupt)
			continue
		}
		state := "unresolved"
		current, identityErr := s.native.Identity(ctx, e.instance)
		if os.IsNotExist(identityErr) {
			observed, inspectErr := s.native.Inspect(ctx, identity)
			if inspectErr == nil && !observed.ProcessRunning && observed.State != domain.StateUnknown {
				state = "dead"
			}
		} else if identityErr == nil && current != identity {
			// Inspect the persisted old identity before classifying replacement: an
			// unverifiable still-live old JVM must retain the saving repair warning.
			observed, inspectErr := s.native.Inspect(ctx, identity)
			if inspectErr == nil && !observed.ProcessRunning && observed.State != domain.StateUnknown {
				state = "replaced"
			}
		} else if identityErr == nil {
			observed, inspectErr := s.native.Inspect(ctx, identity)
			if inspectErr == nil && observed.State != domain.StateUnknown {
				if !observed.ProcessRunning {
					state = "dead"
				} else if s.console != nil {
					cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					result, repairErr := s.console.RecoverSaving(cleanup, identity, e.id)
					cancel()
					if result.Verified && (repairErr == nil || !time.Now().Before(expiry)) {
						state = "released"
					}
				}
			}
		}
		_, writeErr := s.db.ExecContext(ctx, `UPDATE backup_leases SET state=?,updated_at=? WHERE id=?`, state, stamp(), e.id)
		all = errors.Join(all, writeErr)
		if state == "unresolved" {
			all = errors.Join(all, domain.E(domain.CodeUnavailable, "abandoned save lease requires saving recovery before new work"))
		}
	}
	return all
}
func (s *Service) Reconcile(ctx context.Context) error {
	leaseErr := s.ReconcileLeases(ctx)
	if leaseErr != nil {
		return leaseErr
	}
	return s.ReconcileRestores(ctx)
}
