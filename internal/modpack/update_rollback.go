package modpack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

// RollbackParticipant restores the historical baseline itself, including local
// override choices. Restored files need not equal original upstream hashes.
type RollbackParticipant struct {
	owner         *RollbackParticipant
	active        atomic.Bool
	mu            sync.Mutex
	scope         *instance.TransactionScope
	id            string
	actor         domain.User
	before, after *Baseline
}

func (s *Service) WithRollbackParticipant(ctx context.Context, scope *instance.TransactionScope, before, after *Baseline, actor domain.User, fn func(*RollbackParticipant) error) error {
	if fn == nil {
		return invalid("rollback pack callback required")
	}
	raw, err := json.Marshal(struct{ Before, After *Baseline }{before, after})
	if err != nil {
		return err
	}
	var frozen struct{ Before, After *Baseline }
	if err = json.Unmarshal(raw, &frozen); err != nil {
		return err
	}
	p := &RollbackParticipant{scope: scope, before: frozen.Before, after: frozen.After, actor: actor}
	p.owner = p
	s.mu.Lock()
	defer s.mu.Unlock()
	err = scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		p.id = snapshot.Detail.Instance.ID
		if _, err := s.actor(operation, actor.ID, true); err != nil {
			return err
		}
		current, err := readRollbackBaseline(operation, s.db, p.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, p.before) {
			return conflict("current pack baseline changed")
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

type rollbackReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRollbackBaseline(ctx context.Context, reader rollbackReader, id string) (*Baseline, error) {
	var raw string
	err := reader.QueryRowContext(ctx, "SELECT baseline_json FROM modpack_baselines WHERE instance_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var baseline Baseline
	if err = json.Unmarshal([]byte(raw), &baseline); err != nil {
		return nil, err
	}
	return &baseline, nil
}
func (p *RollbackParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	if p == nil || p.owner != p || !p.active.Load() {
		return domain.E(domain.CodeForbidden, "rollback pack capability copied or expired")
	}
	if !p.mu.TryLock() {
		return domain.E(domain.CodeInstanceBusy, "rollback pack operation overlaps")
	}
	defer p.mu.Unlock()
	if !p.active.Load() {
		return domain.E(domain.CodeForbidden, "rollback pack capability expired")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.actor.ID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
			return domain.E(domain.CodeForbidden, "rollback pack administrator revoked")
		}
		current, err := readRollbackBaseline(operation, tx, p.id)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(current, p.before) {
			return conflict("pack baseline changed before rollback commit")
		}
		if p.after == nil {
			_, err = tx.ExecContext(operation, "DELETE FROM modpack_baselines WHERE instance_id=?", p.id)
			return err
		}
		raw, err := json.Marshal(p.after)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(operation, "INSERT INTO modpack_baselines(instance_id,baseline_json) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET baseline_json=excluded.baseline_json", p.id, string(raw))
		return err
	})
}
