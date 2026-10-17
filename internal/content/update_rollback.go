package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

// RollbackParticipant restores exact historical provenance after the backup
// engine restores bytes. It refreshes language evidence only, never Unknown.
type RollbackParticipant struct {
	owner      *RollbackParticipant
	active     atomic.Bool
	mu         sync.Mutex
	scope      *instance.TransactionScope
	target     *lifecycle.RollbackParticipant
	service    *Service
	expected   Manifest
	historical Manifest
	id         string
	actor      domain.User
	pack       *PackParticipant
}

func (s *Service) WithRollbackParticipant(ctx context.Context, scope *instance.TransactionScope, expected, historical Manifest, target *lifecycle.RollbackParticipant, actor domain.User, fn func(*RollbackParticipant) error) error {
	if fn == nil || target == nil {
		return invalid("historical content callback and target required")
	}
	raw, err := json.Marshal(struct{ Expected, Historical Manifest }{expected, historical})
	if err != nil {
		return err
	}
	var frozen struct{ Expected, Historical Manifest }
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return err
	}
	p := &RollbackParticipant{scope: scope, target: target, service: s, expected: frozen.Expected, historical: frozen.Historical, actor: actor}
	p.owner = p
	expected, historical = p.expected, p.historical
	s.mu.Lock()
	defer s.mu.Unlock()
	err = scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		p.id = snapshot.Detail.Instance.ID
		if _, err := s.actor(operation, actor.ID, true); err != nil {
			return err
		}
		current, err := s.Snapshot(operation, p.id)
		if err != nil {
			return err
		}
		if current.Revision != expected.Revision || current.Tuple != expected.Tuple {
			return conflict("current content history changed")
		}
		encoded, _ := json.Marshal(struct {
			Tuple Tuple
			Items []Installed
		}{historical.Tuple, historical.Items})
		if digest(encoded) != historical.Revision {
			return conflict("historical manifest identity differs")
		}
		return nil
	})
	if err != nil {
		return err
	}
	inner := &PackParticipant{}
	state := &packScope{owner: inner, ctx: ctx, service: s, id: p.id, actor: actor, expected: p.expected}
	inner.scope = state
	p.pack = inner
	state.active.Store(true)
	defer func() { state.active.Store(false); state.mu.Lock(); state.mu.Unlock() }()
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *RollbackParticipant) enter() (func(), error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return nil, domain.E(domain.CodeForbidden, "rollback content capability copied or expired")
	}
	if !p.mu.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "rollback content operation overlaps")
	}
	if !p.active.Load() {
		p.mu.Unlock()
		return nil, domain.E(domain.CodeForbidden, "rollback content capability expired")
	}
	return p.mu.Unlock, nil
}
func (p *RollbackParticipant) ValidatePostimages(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	p.pack.scope.validated = false
	return p.target.WithTargetProof(ctx, func(operation context.Context, proof lifecycle.TargetProof) error {
		if _, err := p.service.actor(operation, p.actor.ID, true); err != nil {
			return err
		}
		candidate, err := rollbackCandidate(proof, p.historical)
		if err != nil {
			return err
		}
		p.pack.scope.candidate = candidate
		return p.pack.ValidatePostimages(operation)
	})
}

// Pure conversion of historical diagnostics, conveying no mutation authority.
// The participant obtains proof only from its original historical lifecycle
// capability, after native verification, and validates bytes separately.
func rollbackCandidate(proof lifecycle.TargetProof, historical Manifest) ([]Installed, error) {
	target := Tuple{proof.Selection.MinecraftVersion, string(proof.Selection.Flavor), proof.Selection.LoaderVersion}
	if target != historical.Tuple {
		return nil, conflict("historical content tuple differs from verified target")
	}
	raw, err := json.Marshal(historical.Items)
	if err != nil {
		return nil, err
	}
	var candidate []Installed
	if err = json.Unmarshal(raw, &candidate); err != nil {
		return nil, err
	}
	for i := range candidate {
		candidate[i].LanguageEvidence = nil
		if candidate[i].Descriptor.LanguageRequirement != "" && candidate[i].Descriptor.LanguageRequirement != "*" {
			evidence := proof.Language
			candidate[i].LanguageEvidence = &evidence
		}
	}
	if len(ValidatePackCandidate(target, candidate)) != 0 {
		return nil, conflict("restored historical content is incompatible with verified target")
	}
	return candidate, nil
}
func (p *RollbackParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.pack.scope.validated {
		return conflict("restored historical content postimages not verified")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.actor.ID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "rollback content administrator revoked")
		}
		rows, err := tx.QueryContext(operation, "SELECT manifest_json FROM content_items WHERE instance_id=? ORDER BY id", p.id)
		if err != nil {
			return err
		}
		items := []Installed{}
		for rows.Next() {
			var raw string
			var item Installed
			if err = rows.Scan(&raw); err != nil {
				break
			}
			if err = json.Unmarshal([]byte(raw), &item); err != nil {
				break
			}
			items = append(items, item)
		}
		rowsErr := rows.Err()
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if rowsErr != nil {
			return rowsErr
		}
		if closeErr != nil {
			return closeErr
		}
		raw, _ := json.Marshal(struct {
			Tuple Tuple
			Items []Installed
		}{p.expected.Tuple, items})
		if digest(raw) != p.expected.Revision {
			return conflict("current content changed before rollback commit")
		}
		if _, err = tx.ExecContext(operation, "DELETE FROM content_items WHERE instance_id=?", p.id); err != nil {
			return err
		}
		for _, item := range p.pack.scope.candidate {
			raw, err := json.Marshal(item)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(operation, "INSERT INTO content_items(instance_id,id,manifest_json) VALUES(?,?,?)", p.id, item.ID, string(raw)); err != nil {
				return err
			}
		}
		p.pack.scope.validated = false
		return nil
	})
}
