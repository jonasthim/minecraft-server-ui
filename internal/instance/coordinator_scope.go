package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// ScopeSnapshot is a defensive copy of the admitted identity and paths. Its
// exported data is diagnostic; authority comes only from the original scope.
type ScopeSnapshot struct {
	Detail Detail
	Paths  Directories
}

type coordinatorState struct {
	owner      *TransactionScope
	service    *Service
	ctx        context.Context
	snapshot   ScopeSnapshot
	mu         sync.Mutex
	changed    *sync.Cond
	active     bool
	uses       int
	err        error
	committing bool
	commitUsed bool
	tx         *sql.Tx
	txClosing  bool
	txUses     int
	txErr      error
}

// TransactionScope retains one original stopped session for all participants.
// Only WithCommit opens SQL, on the instance service's original private DB.
// Participant factories must use this scope, never reenter the parent session.
type TransactionScope struct{ state *coordinatorState }

func (s *StoppedSession) WithCoordinatorScope(ctx context.Context, fn func(context.Context, *TransactionScope) error) (result error) {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "coordinator callback required")
	}
	parent, release, err := s.enter(ctx)
	if err != nil {
		return err
	}
	defer release()
	operation, cancel := sessionContext(ctx, parent.ctx)
	defer cancel()
	detail, err := parent.service.Get(operation, parent.id)
	if err != nil {
		return err
	}
	paths, err := parent.service.paths.For(parent.id)
	if err != nil {
		return err
	}
	scope := &TransactionScope{}
	state := &coordinatorState{owner: scope, service: parent.service, ctx: operation, snapshot: ScopeSnapshot{detail, paths}, active: true}
	state.changed = sync.NewCond(&state.mu)
	scope.state = state
	defer func() {
		state.mu.Lock()
		state.active = false
		for state.uses > 0 {
			state.changed.Wait()
		}
		result = errors.Join(result, state.err)
		state.mu.Unlock()
	}()
	return fn(operation, scope)
}
func (s *TransactionScope) identity() (*coordinatorState, error) {
	if s == nil || s.state == nil || s.state.owner != s {
		return nil, domain.E(domain.CodeForbidden, "coordinator scope is forged or copied")
	}
	return s.state, nil
}
func cloneScopeSnapshot(snapshot ScopeSnapshot) ScopeSnapshot {
	raw, _ := json.Marshal(snapshot.Detail)
	var detail Detail
	_ = json.Unmarshal(raw, &detail)
	return ScopeSnapshot{detail, snapshot.Paths}
}

// WithUse admits short-lived participant work without holding an inner mutex
// through its callback. No SQL/service reads are admitted during WithCommit.
// The outer scope drains admitted work even if a caller detaches a goroutine.
func (s *TransactionScope) WithUse(ctx context.Context, fn func(context.Context, ScopeSnapshot) error) (result error) {
	state, err := s.identity()
	if err != nil {
		return err
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "coordinator operation required")
	}
	state.mu.Lock()
	if !state.active {
		state.mu.Unlock()
		return domain.E(domain.CodeForbidden, "coordinator scope expired")
	}
	if state.committing {
		state.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "SQL commit excludes service admission")
	}
	if err = errors.Join(ctx.Err(), state.ctx.Err()); err != nil {
		state.mu.Unlock()
		return err
	}
	state.uses++
	snapshot := cloneScopeSnapshot(state.snapshot)
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.uses--
		state.err = errors.Join(state.err, result)
		state.changed.Broadcast()
		state.mu.Unlock()
	}()
	operation, cancel := sessionContext(ctx, state.ctx)
	defer cancel()
	if err = operation.Err(); err != nil {
		return err
	}
	return fn(operation, snapshot)
}

// WithCommitUse accepts only the exact live transaction supplied by WithCommit.
// Its callback must issue only tx operations; service methods are excluded.
func (s *TransactionScope) WithCommitUse(ctx context.Context, tx *sql.Tx, fn func(context.Context, ScopeSnapshot) error) (result error) {
	state, err := s.identity()
	if err != nil {
		return err
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "SQL participant required")
	}
	state.mu.Lock()
	if !state.active || !state.committing || state.txClosing || tx == nil || tx != state.tx {
		state.mu.Unlock()
		return domain.E(domain.CodeForbidden, "foreign or expired coordinator transaction")
	}
	if err = errors.Join(ctx.Err(), state.ctx.Err()); err != nil {
		state.mu.Unlock()
		return err
	}
	state.txUses++
	snapshot := cloneScopeSnapshot(state.snapshot)
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.txUses--
		state.txErr = errors.Join(state.txErr, result)
		state.changed.Broadcast()
		state.mu.Unlock()
	}()
	operation, cancel := sessionContext(ctx, state.ctx)
	defer cancel()
	return fn(operation, snapshot)
}

// WithCommit closes participant admission and drains detached operations before
// commit/rollback. All pointer contributions therefore share one durable tx.
func (s *TransactionScope) WithCommit(ctx context.Context, fn func(context.Context, *sql.Tx) error) (result error) {
	state, err := s.identity()
	if err != nil {
		return err
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "SQL coordinator callback required")
	}
	state.mu.Lock()
	if !state.active {
		state.mu.Unlock()
		return domain.E(domain.CodeForbidden, "coordinator scope expired")
	}
	if state.committing || state.commitUsed || state.uses != 0 {
		state.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "coordinator operation overlaps SQL commit")
	}
	if err = errors.Join(ctx.Err(), state.ctx.Err()); err != nil {
		state.mu.Unlock()
		return err
	}
	state.commitUsed = true
	state.committing = true
	state.txClosing = false
	state.txErr = nil
	state.uses++
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.committing = false
		state.tx = nil
		state.uses--
		state.err = errors.Join(state.err, result)
		state.changed.Broadcast()
		state.mu.Unlock()
	}()
	operation, cancel := sessionContext(ctx, state.ctx)
	defer cancel()
	tx, err := state.service.db.BeginTx(operation, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var synchronous int
	if err = tx.QueryRowContext(operation, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous < 2 {
		return domain.E(domain.CodeUnavailable, "coordinator commit requires FULL SQLite durability")
	}
	state.mu.Lock()
	state.tx = tx
	state.mu.Unlock()
	// Drain before the deferred rollback also if fn panics.
	callbackErr := func() (callbackErr error) {
		defer func() {
			state.mu.Lock()
			state.txClosing = true
			for state.txUses > 0 {
				state.changed.Wait()
			}
			callbackErr = errors.Join(callbackErr, state.txErr)
			state.mu.Unlock()
		}()
		return fn(operation, tx)
	}()
	if callbackErr != nil {
		return callbackErr
	}
	return tx.Commit()
}
