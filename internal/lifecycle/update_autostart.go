package lifecycle

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

// AutostartSuspension is a durable policy preimage, never launch authority.
// Desired administrator policy is preserved throughout temporary suspension.
type AutostartSuspension struct {
	PriorPresent     bool   `json:"prior_present"`
	PriorDesired     bool   `json:"prior_desired"`
	PriorApplied     *bool  `json:"prior_applied"`
	PriorError       string `json:"prior_error"`
	DisableConfirmed bool   `json:"disable_confirmed"`
	RestoreConfirmed bool   `json:"restore_confirmed"`
}

func (s *Service) updateAutostartIntent(ctx context.Context, id, transaction string) (AutostartSuspension, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT journal_json FROM update_transactions WHERE id=? AND instance_id=? AND kind='update' AND state IN ('prepared','publishing','committed','boot_attempted','blocked','rolled_back','complete')`, transaction, id).Scan(&raw); err != nil {
		return AutostartSuspension{}, err
	}
	var receipt struct {
		Version   int                  `json:"version"`
		Autostart *AutostartSuspension `json:"autostart"`
	}
	if len(raw) > 32<<20 || json.Unmarshal([]byte(raw), &receipt) != nil || receipt.Version != 1 || receipt.Autostart == nil {
		return AutostartSuspension{}, domain.E(domain.CodeConflict, "durable autostart suspension receipt required")
	}
	return *receipt.Autostart, nil
}
func (s *Service) suspendUpdatePolicy(ctx context.Context, id, transaction string) (AutostartSuspension, error) {
	policy, err := s.updateAutostartIntent(ctx, id, transaction)
	if err != nil {
		return policy, err
	}
	if err = s.native.SetAutostart(ctx, id, false); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(cleanup, `INSERT INTO lifecycle_autostart(instance_id,desired,applied,error) VALUES(?,?,NULL,?) ON CONFLICT(instance_id) DO UPDATE SET applied=NULL,error=excluded.error`, id, policy.PriorDesired, "Native autostart suspension failed; applied policy is unknown and update remains blocked.")
		return policy, domain.E(domain.CodeUnavailable, "Could not suspend independent native autostart; update remains blocked before activation.")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lifecycle_autostart(instance_id,desired,applied,error) VALUES(?,?,0,?) ON CONFLICT(instance_id) DO UPDATE SET applied=0,error=excluded.error`, id, policy.PriorDesired, "Autostart suspended during instance update; retained recovery must finish before re-enabling.")
	if err != nil {
		return policy, err
	}
	policy.DisableConfirmed = true
	policy.RestoreConfirmed = false
	return policy, nil
}

// SuspendUpdateAutostart runs before retained native stopped admission. Calling
// SetAutostart from inside native.WithStopped would reacquire its same mutex.
func (s *Service) SuspendUpdateAutostart(ctx context.Context, e *jobs.Execution, actor domain.User, transaction string) (policy AutostartSuspension, result error) {
	current, err := e.CheckCurrent(ctx)
	if err != nil {
		return policy, err
	}
	if current.Kind != "instance_update" || current.RequesterID != actor.ID {
		return policy, domain.E(domain.CodeForbidden, "original update autostart authority required")
	}
	result = e.WithInstanceMutation(ctx, func(id string) error {
		if err := s.actor(ctx, actor.ID, false); err != nil {
			return err
		}
		var exact bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM update_transactions WHERE id=? AND instance_id=? AND job_id=? AND actor_id=? AND kind='update' AND state='prepared')", transaction, id, current.JobID, actor.ID).Scan(&exact); err != nil {
			return err
		}
		if !exact {
			return domain.E(domain.CodeConflict, "exact prepared update required before autostart suspension")
		}
		var err error
		policy, err = s.suspendUpdatePolicy(ctx, id, transaction)
		return err
	})
	return
}

// ReassertUpdateAutostart is used only by the paused startup coordinator before
// it enters any native stopped scope. The bound service DB supplies the intent.
func (s *Service) ReassertUpdateAutostart(ctx context.Context, id, transaction string) (AutostartSuspension, error) {
	return s.suspendUpdatePolicy(ctx, id, transaction)
}
func (s *Service) RestoreUpdateAutostart(ctx context.Context, e *jobs.Execution, actor domain.User, transaction string) (policy AutostartSuspension, result error) {
	current, err := e.CheckCurrent(ctx)
	if err != nil {
		return policy, err
	}
	if current.Kind != "instance_update" || current.RequesterID != actor.ID {
		return policy, domain.E(domain.CodeForbidden, "original update policy restoration authority required")
	}
	result = e.WithInstanceMutation(ctx, func(id string) error {
		if err := s.actor(ctx, actor.ID, false); err != nil {
			return err
		}
		var owned bool
		if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM update_transactions WHERE id=? AND instance_id=? AND job_id=? AND actor_id=? AND kind='update')", transaction, id, current.JobID, actor.ID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return domain.E(domain.CodeForbidden, "original update policy receipt required")
		}

		var err error
		policy, err = s.restoreUpdatePolicy(ctx, id, transaction)
		return err
	})
	return
}
func (s *Service) RestoreRecoveredUpdateAutostart(ctx context.Context, id, transaction string) (AutostartSuspension, error) {
	return s.restoreUpdatePolicy(ctx, id, transaction)
}
func (s *Service) restoreUpdatePolicy(ctx context.Context, id, transaction string) (AutostartSuspension, error) {
	policy, err := s.updateAutostartIntent(ctx, id, transaction)
	if err != nil {
		return policy, err
	}
	if !policy.DisableConfirmed {
		return policy, domain.E(domain.CodeConflict, "autostart suspension not confirmed")
	}
	var desired bool
	if err = s.db.QueryRowContext(ctx, "SELECT desired FROM lifecycle_autostart WHERE instance_id=?", id).Scan(&desired); err != nil {
		return policy, err
	}
	if desired != policy.PriorDesired {
		return policy, domain.E(domain.CodeConflict, "administrator autostart policy changed after update intent")
	}
	enabled := policy.PriorApplied != nil && *policy.PriorApplied
	if err = s.native.SetAutostart(ctx, id, enabled); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = s.db.ExecContext(cleanup, "UPDATE lifecycle_autostart SET applied=NULL,error=? WHERE instance_id=?", "Update pointers verified, but native autostart policy restoration failed and actual policy is unknown. Inspect before re-enabling.", id)
		return policy, domain.E(domain.CodeUnavailable, "Update applied, but native autostart policy restoration failed; inspect and re-enable explicitly.")
	}
	// Unknown preexisting applied policy remains safely disabled and visible.
	message := policy.PriorError
	if policy.PriorApplied == nil && policy.PriorPresent {
		message = "Previous native autostart policy was unknown; autostart remains disabled. Re-enable explicitly after inspection."
	}
	_, err = s.db.ExecContext(ctx, "UPDATE lifecycle_autostart SET applied=?,error=? WHERE instance_id=?", enabled, message, id)
	if err != nil {
		return policy, err
	}
	policy.RestoreConfirmed = true
	return policy, nil
}
