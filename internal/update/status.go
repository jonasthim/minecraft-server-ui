package update

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type History struct {
	ID                  string                  `json:"id"`
	JobID               string                  `json:"job_id"`
	Kind                string                  `json:"kind"`
	State               string                  `json:"state"`
	From                domain.VersionSelection `json:"from"`
	Target              domain.VersionSelection `json:"target"`
	BackupID            string                  `json:"backup_id"`
	BootAttempted       bool                    `json:"boot_attempted"`
	ParentTransactionID string                  `json:"parent_transaction_id,omitempty"`
	SupersededBy        string                  `json:"superseded_by,omitempty"`
	UpdatedAt           time.Time               `json:"updated_at"`
}
type Status struct {
	History       []History `json:"history"`
	Blockers      []string  `json:"blockers"`
	Qualification string    `json:"qualification"`
}

func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	out := Status{History: []History{}, Blockers: []string{}, Qualification: "Compatibility and verified downloads do not establish native game/platform qualification. World data is never rewound automatically."}
	if _, err := s.config.Instances.Get(ctx, id); err != nil {
		return out, err
	}
	rows, err := s.config.DB.QueryContext(ctx, `SELECT id,job_id,kind,state,backup_id,coalesce(parent_transaction_id,''),coalesce(superseded_by,''),journal_json,updated_at FROM update_transactions WHERE instance_id=? ORDER BY updated_at DESC,id DESC LIMIT 64`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var row History
		var raw, updated string
		if err = rows.Scan(&row.ID, &row.JobID, &row.Kind, &row.State, &row.BackupID, &row.ParentTransactionID, &row.SupersededBy, &raw, &updated); err != nil {
			break
		}
		row.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		var history struct {
			Plan          Plan `json:"plan"`
			BootAttempted bool `json:"boot_attempted"`
		}
		if json.Unmarshal([]byte(raw), &history) == nil {
			row.From = history.Plan.From
			row.Target = history.Plan.Target
			row.BootAttempted = history.BootAttempted
		}
		out.History = append(out.History, row)
	}
	if err == nil {
		err = rows.Err()
	}
	if closeErr := rows.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return out, err
	}
	var recoveryRaw string
	if readErr := s.config.DB.QueryRowContext(ctx, "SELECT journal_json FROM update_transactions WHERE instance_id=? AND kind='update' AND state='blocked'", id).Scan(&recoveryRaw); readErr == nil {
		var observation struct {
			Note string `json:"recovery_observation"`
		}
		if json.Unmarshal([]byte(recoveryRaw), &observation) == nil && observation.Note != "" {
			out.Blockers = append(out.Blockers, observation.Note)
		}
	}
	var policyError string
	if readErr := s.config.DB.QueryRowContext(ctx, "SELECT error FROM lifecycle_autostart WHERE instance_id=?", id).Scan(&policyError); readErr == nil && policyError != "" {
		out.Blockers = append(out.Blockers, policyError)
	}
	if err = s.CheckReady(ctx, id); err != nil {
		out.Blockers = append(out.Blockers, domain.AsError(err).Message)
	}
	if err = s.checkOrdinaryReady(ctx, id); err != nil {
		out.Blockers = append(out.Blockers, domain.AsError(err).Message)
	}
	return out, nil
}
func (s *Service) PreviewNeedsAdministrator(ctx context.Context, id string, request Request, actor int64) (bool, error) {
	if len(request.ContentManual) > 0 {
		return true, nil
	}
	if request.Pack == nil {
		return false, nil
	}
	return s.config.Packs.RequestNeedsAdministrator(ctx, id, *request.Pack, actor)
}
func (s *Service) ApplyNeedsAdministrator(ctx context.Context, id, preview string, actor int64) (bool, error) {
	envelope, err := s.loadPreview(ctx, id, preview, actor)
	if err != nil {
		return false, err
	}
	return s.PreviewNeedsAdministrator(ctx, id, envelope.Request, actor)
}
