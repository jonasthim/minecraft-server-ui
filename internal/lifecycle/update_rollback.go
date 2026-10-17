package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
)

// RollbackParticipant restores a retained installation into a NEW generation.
// Historical generation rows are never overwritten, including memory bindings.
type RollbackParticipant struct {
	owner                                      *RollbackParticipant
	active                                     atomic.Bool
	mu                                         sync.Mutex
	service                                    *Service
	scope                                      *instance.TransactionScope
	id, transaction, job, expected, historyRaw string
	actor                                      domain.User
	historical                                 UpdateState
	next                                       UpdateState
	sealed                                     bool
}

func (s *Service) WithRollbackParticipant(ctx context.Context, scope *instance.TransactionScope, transaction, expected string, historical UpdateState, actor domain.User, fn func(*RollbackParticipant) error) error {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "rollback lifecycle callback required")
	}
	p := &RollbackParticipant{service: s, scope: scope, transaction: transaction, expected: expected, historical: cloneUpdateState(historical), actor: actor}
	p.owner = p
	historical = p.historical
	err := scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		p.id = snapshot.Detail.Instance.ID
		if err := s.actor(operation, actor.ID, true); err != nil {
			return err
		}
		var raw string
		if err := s.db.QueryRowContext(operation, "SELECT installation_json FROM lifecycle_generations WHERE instance_id=? AND generation=?", p.id, historical.Generation).Scan(&raw); err != nil {
			return err
		}
		var retained installation
		if json.Unmarshal([]byte(raw), &retained) != nil || retained.Generation != historical.Generation || !reflect.DeepEqual(retained.Plan, historical.Plan) || !reflect.DeepEqual(retained.Spec, historical.Spec) {
			return domain.E(domain.CodeConflict, "historical installation changed")
		}
		p.historyRaw = raw
		current, err := s.loadInstallation(operation, p.id)
		if err != nil {
			return err
		}
		if current.Generation != expected {
			return domain.E(domain.CodeConflict, "rollback current installation changed")
		}
		var kind, parent string
		if err = s.db.QueryRowContext(operation, "SELECT kind,job_id,parent_transaction_id FROM update_transactions WHERE id=? AND instance_id=? AND actor_id=? AND state NOT IN ('superseded','rolled_back')", transaction, p.id, actor.ID).Scan(&kind, &p.job, &parent); err != nil {
			return err
		}
		if kind != "instance_rollback" || parent == "" {
			return domain.E(domain.CodeForbidden, "exact rollback history owner required")
		}
		if s.runtime == nil {
			return domain.E(domain.CodeUnavailable, "historical runtime verification unavailable")
		}
		managed, err := s.runtime.Get(operation, retained.Spec.JavaRuntimeID)
		if err != nil {
			return err
		}
		if managed.ID != historical.Runtime.ID || managed.Artifact.Checksum != retained.Spec.JavaChecksum || !reflect.DeepEqual(managed, historical.Runtime) {
			return domain.E(domain.CodeConflict, "historical managed runtime changed")
		}
		base, err := stripBoundMemory(retained.Spec)
		if err != nil {
			return err
		}
		spec, err := bindMemory(base, snapshot.Detail.Instance.MemoryMiB)
		if err != nil {
			return err
		}
		p.next = cloneUpdateState(UpdateState{Generation: transaction, Plan: retained.Plan, Spec: spec, Runtime: managed})
		if expected == transaction && (!reflect.DeepEqual(current.Plan, p.next.Plan) || !reflect.DeepEqual(current.Spec, p.next.Spec)) {
			return domain.E(domain.CodeConflict, "committed rollback executable identity changed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *RollbackParticipant) enter() (func(), error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return nil, domain.E(domain.CodeForbidden, "rollback lifecycle capability copied or expired")
	}
	if !p.mu.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "rollback lifecycle operation overlaps")
	}
	if !p.active.Load() {
		p.mu.Unlock()
		return nil, domain.E(domain.CodeForbidden, "rollback lifecycle capability expired")
	}
	return p.mu.Unlock, nil
}
func (p *RollbackParticipant) State() (UpdateState, error) {
	release, err := p.enter()
	if err != nil {
		return UpdateState{}, err
	}
	defer release()
	return cloneUpdateState(p.next), nil
}
func (p *RollbackParticipant) Seal(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	return p.scope.WithUse(ctx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		marker, present, err := launcher.ReadUpdateBlocker(operation, p.service.dataDir, p.id)
		if err != nil {
			return err
		}
		if !present || marker.TransactionID != p.transaction || marker.JobID != p.job || marker.Kind != "instance_rollback" {
			return domain.E(domain.CodeConflict, "exact rollback blocker required before historical seal")
		}
		if err = p.service.actor(operation, p.actor.ID, true); err != nil {
			return err
		}
		// Production seal rehashes BOTH managed runtime and immutable provider tree.
		if err = p.service.seal(operation, p.id, p.next.Spec); err != nil {
			return err
		}
		p.sealed = true
		return nil
	})
}

// WithTargetProof obtains fresh evidence from the sealed historical loader,
// independently of the still-current failed update's selection.
func (p *RollbackParticipant) WithTargetProof(ctx context.Context, fn func(context.Context, TargetProof) error) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.sealed || fn == nil {
		return domain.E(domain.CodeConflict, "verified historical seal required")
	}
	return p.scope.WithUse(ctx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		if err := p.service.actor(operation, p.actor.ID, true); err != nil {
			return err
		}
		managed, err := p.service.runtime.Get(operation, p.next.Runtime.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(managed, p.next.Runtime) {
			return domain.E(domain.CodeConflict, "historical runtime proof changed")
		}
		if err = p.service.seal(operation, p.id, p.next.Spec); err != nil {
			return err
		}
		language := loaderutil.LanguageEvidence{Selection: p.next.Plan.Selection, RuntimeID: managed.ID, JavaMajor: managed.Major, Languages: map[string]loaderutil.LanguageArtifact{}}
		if p.next.Plan.Selection.Flavor == domain.FlavorForge || p.next.Plan.Selection.Flavor == domain.FlavorNeoForge {
			language, err = loaderutil.VerifiedLanguageEvidence(operation, p.service.dataDir, p.id, p.next.Spec)
			if err != nil {
				return err
			}
		}
		return fn(operation, TargetProof{Selection: p.next.Plan.Selection, Requirements: p.next.Plan.Requirements, Runtime: managed, Language: language})
	})
}
func (p *RollbackParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.sealed {
		return domain.E(domain.CodeConflict, "historical executable not sealed")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		var generation, history string
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT generation FROM lifecycle_installations WHERE instance_id=?", p.id).Scan(&generation); err != nil {
			return err
		}
		if generation != p.expected {
			return domain.E(domain.CodeConflict, "rollback generation changed before commit")
		}
		if err := tx.QueryRowContext(operation, "SELECT installation_json FROM lifecycle_generations WHERE instance_id=? AND generation=?", p.id, p.historical.Generation).Scan(&history); err != nil {
			return err
		}
		if history != p.historyRaw {
			return domain.E(domain.CodeConflict, "historical installation changed before commit")
		}
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.actor.ID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "rollback administrator revoked")
		}
		next := installation{Generation: p.next.Generation, Plan: p.next.Plan, Spec: p.next.Spec}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		return saveInstallationTx(operation, tx, p.id, next, raw)
	})
}
