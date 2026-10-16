package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events"
	"time"
)

const columns = `id,COALESCE(instance_id,''),kind,state,phase,error,created_at,started_at,finished_at,requested_by,progress,cancellable,cancel_requested`

type scanner interface{ Scan(...any) error }

func scanJob(row scanner) (Job, error) {
	var j Job
	var created string
	var started, finished sql.NullString
	err := row.Scan(&j.ID, &j.InstanceID, &j.Kind, &j.State, &j.Phase, &j.Error, &created, &started, &finished, &j.RequestedBy, &j.Progress, &j.Cancellable, &j.CancelRequested)
	if err != nil {
		return j, err
	}
	var e error
	j.CreatedAt, e = time.Parse(time.RFC3339Nano, created)
	if e != nil {
		return j, e
	}
	if started.Valid {
		t, e := time.Parse(time.RFC3339Nano, started.String)
		if e != nil {
			return j, e
		}
		j.StartedAt = &t
	}
	if finished.Valid {
		t, e := time.Parse(time.RFC3339Nano, finished.String)
		if e != nil {
			return j, e
		}
		j.FinishedAt = &t
	}
	return j, nil
}
func (s *Service) Get(ctx context.Context, id string) (Job, error) {
	if !identifier.MatchString(id) {
		return Job{}, domain.E(domain.CodeValidationFailed, "invalid job identifier")
	}
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM jobs WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return j, domain.NotFound("job")
	}
	if err != nil {
		return j, fmt.Errorf("get job: %w", err)
	}
	return j, nil
}

// List returns a bounded newest-first page, optionally restricted to an instance.
func (s *Service) List(ctx context.Context, instanceID string) ([]Job, error) {
	if instanceID != "" && !identifier.MatchString(instanceID) {
		return nil, domain.E(domain.CodeValidationFailed, "invalid instance identifier")
	}
	return s.list(ctx, `WHERE (?='' OR instance_id=?) ORDER BY created_at DESC,rowid DESC LIMIT 100`, instanceID, instanceID)
}
func (s *Service) list(ctx context.Context, where string, args ...any) ([]Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM jobs `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	out := make([]Job, 0)
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// Logs is a bounded durable page. Raw request payloads and raw source errors are excluded.
func (s *Service) Logs(ctx context.Context, id string, after int64, limit int) ([]events.Log, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	if after < 0 || limit < 1 || limit > 500 {
		return nil, domain.E(domain.CodeValidationFailed, "invalid log cursor or limit")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,created_at,message FROM job_logs WHERE job_id=? AND id>? ORDER BY id LIMIT ?`, id, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list job logs: %w", err)
	}
	defer rows.Close()
	logs := make([]events.Log, 0)
	for rows.Next() {
		var line events.Log
		var ts string
		line.JobID = id
		if err := rows.Scan(&line.ID, &ts, &line.Message); err != nil {
			return nil, err
		}
		line.CreatedAt, err = time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return nil, err
		}
		logs = append(logs, line)
	}
	return logs, rows.Err()
}
func (s *Service) payload(ctx context.Context, id string) ([]byte, error) {
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM jobs WHERE id=?`, id).Scan(&payload)
	return []byte(payload), err
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
