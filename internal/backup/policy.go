package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type RetentionPolicy struct {
	KeepLast int   `json:"keep_last"`
	KeepDays int   `json:"keep_days"`
	Revision int64 `json:"revision"`
}

type RetentionPolicyInput struct {
	KeepLast         int   `json:"keep_last"`
	KeepDays         int   `json:"keep_days"`
	ExpectedRevision int64 `json:"expected_revision"`
}

func validPolicy(last, days int) bool {
	return last >= 0 && last <= 10000 && days >= 0 && days <= 36500
}

type policyReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRetentionPolicy(ctx context.Context, db policyReader, id string) (RetentionPolicy, error) {
	var policy RetentionPolicy
	err := db.QueryRowContext(ctx, `SELECT keep_last,keep_days,revision FROM backup_retention_policies WHERE instance_id=?`, id).Scan(&policy.KeepLast, &policy.KeepDays, &policy.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return RetentionPolicy{KeepLast: 10, KeepDays: 30}, nil
	}
	if err != nil {
		return policy, fmt.Errorf("read automatic-backup retention policy: %w", err)
	}
	if !validPolicy(policy.KeepLast, policy.KeepDays) || policy.Revision < 1 {
		return RetentionPolicy{}, domain.E(domain.CodeUnavailable, "stored automatic-backup retention policy is invalid; pruning is blocked")
	}
	return policy, nil
}

func (s *Service) GetRetentionPolicy(ctx context.Context, id string) (RetentionPolicy, error) {
	if _, err := s.instances.Get(ctx, id); err != nil {
		return RetentionPolicy{}, err
	}
	return readRetentionPolicy(ctx, s.db, id)
}

// Policy admission always precedes DB admission and the archive-reference mutex.
// Cancellation while queued never takes the sole SQLite connection. Retention
// holds this gate through filesystem deletion, so a newer, looser policy cannot
// be committed while deletions still use an older policy.
func (s *Service) enterPolicy(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.policyGate <- struct{}{}:
	}
	if err := ctx.Err(); err != nil {
		<-s.policyGate
		return err
	}
	return nil
}

func (s *Service) SetRetentionPolicy(ctx context.Context, id string, input RetentionPolicyInput, actor domain.User) (RetentionPolicy, error) {
	if actor.ID <= 0 || actor.Disabled || actor.Role != domain.RoleAdmin {
		return RetentionPolicy{}, domain.E(domain.CodeForbidden, "active administrator access required")
	}
	if !validPolicy(input.KeepLast, input.KeepDays) || input.ExpectedRevision < 0 {
		return RetentionPolicy{}, domain.E(domain.CodeValidationFailed, "retention count must be 0–10000, days 0–36500, and revision nonnegative")
	}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return RetentionPolicy{}, err
	}
	if err := s.enterPolicy(ctx); err != nil {
		return RetentionPolicy{}, err
	}
	defer func() { <-s.policyGate }()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("begin retention policy mutation: %w", err)
	}
	defer tx.Rollback()
	fresh := domain.User{ID: actor.ID}
	if err = tx.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, actor.ID).Scan(&fresh.Username, &fresh.Role, &fresh.Disabled); err != nil || fresh.Disabled || fresh.Role != domain.RoleAdmin {
		return RetentionPolicy{}, domain.E(domain.CodeForbidden, "fresh administrator access required")
	}
	before, err := readRetentionPolicy(ctx, tx, id)
	if err != nil {
		return RetentionPolicy{}, err
	}
	if before.KeepLast == input.KeepLast && before.KeepDays == input.KeepDays {
		return before, nil
	}
	if before.Revision != input.ExpectedRevision {
		return RetentionPolicy{}, domain.E(domain.CodeConflict, "retention policy changed; reload the current policy before saving")
	}
	if before.Revision == math.MaxInt64 {
		return RetentionPolicy{}, domain.E(domain.CodeUnavailable, "retention policy revision exhausted")
	}
	after := RetentionPolicy{KeepLast: input.KeepLast, KeepDays: input.KeepDays, Revision: before.Revision + 1}
	_, err = tx.ExecContext(ctx, `INSERT INTO backup_retention_policies(instance_id,keep_last,keep_days,revision,updated_at,updated_by) VALUES(?,?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET keep_last=excluded.keep_last,keep_days=excluded.keep_days,revision=excluded.revision,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, id, after.KeepLast, after.KeepDays, after.Revision, stamp(), fresh.ID)
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("persist retention policy: %w", err)
	}
	details, _ := json.Marshal(struct {
		Before RetentionPolicy `json:"before"`
		After  RetentionPolicy `json:"after"`
	}{before, after})
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,'backups.retention_policy',?,?,?)`, stamp(), fresh.ID, fresh.Username, id, "automatic_backup_retention", string(details))
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("audit retention policy: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return RetentionPolicy{}, fmt.Errorf("commit retention policy: %w", err)
	}
	return after, nil
}

func (s *Service) applyCurrentRetention(ctx context.Context, id string, now time.Time) error {
	if err := s.enterPolicy(ctx); err != nil {
		return err
	}
	defer func() { <-s.policyGate }()
	policy, err := readRetentionPolicy(ctx, s.db, id)
	if err != nil {
		return err
	}
	return s.applyRetention(ctx, id, Retention{Count: policy.KeepLast, MaxAge: time.Duration(policy.KeepDays) * 24 * time.Hour}, now)
}
