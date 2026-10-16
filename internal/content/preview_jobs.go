package content

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

const contentJobPreview = "json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')"

type previewOwnership struct {
	ActorID       int64
	State, JobID  string
	Live, Journal bool
}

// The durable job reference in its immutable private payload is authoritative,
// including the crash window before job_id can be attached to the preview.
// Callers hold s.mu. A terminal queue state is published only after the worker
// and admitted cleanup have returned; it is safe to release an unjournaled
// preview then. Never release ownership from a handler's own defer.
func (s *Service) reconcilePreviewJob(ctx context.Context, id, preview string) (previewOwnership, error) {
	var owner previewOwnership
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return owner, e
	}
	defer tx.Rollback()
	var bound sql.NullString
	e = tx.QueryRowContext(ctx, "SELECT actor_id,state,job_id FROM content_previews WHERE instance_id=? AND id=?", id, preview).Scan(&owner.ActorID, &owner.State, &bound)
	if errors.Is(e, sql.ErrNoRows) {
		return owner, domain.NotFound("content preview")
	}
	if e != nil {
		return owner, e
	}
	if bound.Valid {
		var valid bool
		e = tx.QueryRowContext(ctx, "SELECT instance_id=? AND kind='content_mutation' AND "+contentJobPreview+"=? AND requested_by=? FROM jobs WHERE id=?", id, preview, owner.ActorID, bound.String).Scan(&valid)
		if e != nil || !valid {
			return owner, conflict("preview job binding is unavailable or inconsistent")
		}
		owner.JobID = bound.String
	}
	var requester int64
	e = tx.QueryRowContext(ctx, "SELECT id,requested_by FROM jobs WHERE instance_id=? AND kind='content_mutation' AND "+contentJobPreview+"=? AND state IN ('queued','running')", id, preview).Scan(&owner.JobID, &requester)
	if e == nil {
		if requester != owner.ActorID {
			return owner, conflict("live job requester differs from preview owner")
		}
		owner.Live = true
		if owner.State != "applied" {
			owner.State = "queued"
		}
		if _, e = tx.ExecContext(ctx, "UPDATE content_previews SET state=?,job_id=? WHERE id=?", owner.State, owner.JobID, preview); e != nil {
			return owner, e
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return owner, e
	}
	var journals int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM content_journals WHERE instance_id=? AND preview_id=?", id, preview).Scan(&journals); e != nil {
		return owner, e
	}
	owner.Journal = journals > 0
	if !owner.Live && !owner.Journal && owner.State == "queued" {
		owner.State = "preview"
		if _, e = tx.ExecContext(ctx, "UPDATE content_previews SET state='preview' WHERE id=?", preview); e != nil {
			return owner, e
		}
	}
	if e = tx.Commit(); e != nil {
		return owner, e
	}
	return owner, nil
}
func (s *Service) reconcileInstancePreviewJobs(ctx context.Context, id string) error {
	rows, e := s.db.QueryContext(ctx, "SELECT id FROM content_previews WHERE instance_id=?", id)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var preview string
		if e = rows.Scan(&preview); e != nil {
			break
		}
		ids = append(ids, preview)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	for _, preview := range ids {
		if _, e = s.reconcilePreviewJob(ctx, id, preview); e != nil {
			return e
		}
	}
	return nil
}
