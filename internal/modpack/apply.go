package modpack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"io"
	"os"
	"strings"
	"time"
)

func (s *Service) loadPreview(ctx context.Context, id, preview string, actor int64) (privatePreview, error) {
	var p privatePreview
	var raw, state, created string
	var owner int64
	e := s.db.QueryRowContext(ctx, "SELECT actor_id,private_json,state,created_at FROM modpack_previews WHERE instance_id=? AND id=?", id, preview).Scan(&owner, &raw, &state, &created)
	if errors.Is(e, sql.ErrNoRows) {
		return p, domain.NotFound("pack preview")
	}
	if e != nil {
		return p, e
	}
	if owner != actor {
		return p, domain.E(domain.CodeForbidden, "pack preview belongs to another actor")
	}
	if created < time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano) || state != "preview" && state != "queued" {
		return p, conflict("pack preview expired or already applied")
	}
	if json.Unmarshal([]byte(raw), &p) != nil || p.ActorID != owner || p.Preview.ID != preview || p.Preview.InstanceID != id {
		return p, conflict("pack preview is corrupt")
	}
	return p, nil
}
func (s *Service) liveJob(ctx context.Context, id, preview string) (string, error) {
	var job string
	e := s.db.QueryRowContext(ctx, "SELECT id FROM jobs WHERE instance_id=? AND kind='modpack_apply' AND json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')=? AND state IN ('queued','running')", id, preview).Scan(&job)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return job, e
}
func (s *Service) Enqueue(ctx context.Context, id, preview string, actor domain.User) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.actor(ctx, actor.ID, false); e != nil {
		return jobs.Job{}, e
	}
	if e := s.CheckReady(ctx, id); e != nil {
		return jobs.Job{}, e
	}
	p, e := s.loadPreview(ctx, id, preview, actor.ID)
	if e != nil {
		return jobs.Job{}, e
	}
	if _, e = s.actor(ctx, actor.ID, len(p.Request.Manual) > 0 || p.Request.ArchiveReceiptID != ""); e != nil {
		return jobs.Job{}, e
	}
	if len(p.Preview.Blockers) > 0 || len(p.Preview.Manual) > 0 {
		return jobs.Job{}, conflict("resolve pack conflicts and manual files before applying")
	}
	if job, e := s.liveJob(ctx, id, preview); e != nil {
		return jobs.Job{}, e
	} else if job != "" {
		return jobs.Job{}, conflict("pack preview already owned by live job")
	}
	payload, _ := json.Marshal(map[string]any{"preview_id": preview, "actor_id": actor.ID})
	return s.queue.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "modpack_apply", Payload: payload, RequestedBy: actor.ID, Cancellable: true})
}
func (s *Service) run(ctx context.Context, execution *jobs.Execution) error {
	var req struct {
		PreviewID string `json:"preview_id"`
		ActorID   int64  `json:"actor_id"`
	}
	if json.Unmarshal(execution.Payload, &req) != nil || req.ActorID <= 0 {
		return invalid("invalid pack job")
	}
	return s.instances.WithStoppedJob(ctx, execution, func(session *instance.StoppedSession) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		p, e := s.loadPreview(ctx, execution.Job.InstanceID, req.PreviewID, req.ActorID)
		if e != nil {
			return e
		}
		id := p.Preview.InstanceID
		if e = s.CheckReady(ctx, id); e != nil {
			return e
		}
		job, e := s.liveJob(ctx, id, p.Preview.ID)
		if e != nil || job != execution.Job.ID {
			return conflict("current job does not own pack preview")
		}
		actor, e := s.actor(ctx, req.ActorID, len(p.Request.Manual) > 0 || p.Request.ArchiveReceiptID != "")
		if e != nil {
			return e
		}
		if len(p.Preview.Blockers) > 0 || len(p.Preview.Manual) > 0 {
			return conflict("pack preview remains unresolved")
		}
		original, artifactVersion, actual, e := s.resolvePackMetadata(ctx, p.Request.Pack)
		if e != nil {
			return e
		}
		var artifact catalog.VerifiedArtifact
		if p.Request.ArchiveReceiptID != "" {
			artifact, e = s.archiveArtifact(ctx, id, p.Request.ArchiveReceiptID, actor.ID, p.Request.Pack, actual, original, artifactVersion)
		} else {
			artifact, e = s.catalog.Download(ctx, actual)
		}
		if e != nil {
			return e
		}
		if packMetadataHash(original, artifactVersion) != p.Metadata || artifact.SHA256 != p.Preview.Source.SHA256 || artifact.Selection != p.Preview.Source.ArtifactSelection {
			return conflict("publisher pack metadata changed")
		}
		snapshot, e := s.Snapshot(ctx, id)
		if e != nil {
			return e
		}
		baselineRevision := ""
		if snapshot.Baseline != nil {
			baselineRevision = snapshot.Baseline.Revision
		}
		if baselineRevision != p.Preview.BaselineRevision {
			return conflict("pack baseline changed")
		}
		result := s.content.WithPackParticipant(ctx, session, p.Manifest, p.Candidate, actor, func(ctx context.Context, d instance.Detail, dirs instance.Directories, participant *content.PackParticipant) error {
			if d.Instance.ID != id {
				return conflict("pack participant instance mismatch")
			}
			root, e := s.openServer(ctx, id)
			if e != nil {
				return e
			}
			defer root.Close()
			for _, change := range p.Preview.Changes {
				actual := physicalPath(change.Path, p.Preview.Selections[change.Path])
				hash, _, e := currentHash(ctx, root, actual, MaxFileBytes)
				if e != nil {
					return e
				}
				if hash != change.Before {
					return conflict("local pack preimage changed since preview")
				}
			}
			for _, op := range p.Operations {
				for _, st := range []stage{op.Before, op.After} {
					if st.ID != "" {
						f, e := s.openStage(ctx, id, st)
						if e != nil {
							return e
						}
						f.Close()
					}
				}
			}
			raw, _ := json.Marshal(p)
			if _, e = s.db.ExecContext(ctx, "INSERT INTO modpack_journals(instance_id,preview_id,private_json,state) VALUES(?,?,?,'applying')", id, p.Preview.ID, string(raw)); e != nil {
				return e
			}
			if e = s.markTransaction(ctx, id, p.Preview.ID); e != nil {
				return e
			}
			if s.fault != nil {
				if e = s.fault("journal"); e != nil {
					return e
				}
			}
			if e = execution.Phase(ctx, "publish_pack", 40); e != nil {
				return s.cleanupFailure(root, id, e)
			}
			for _, op := range p.Operations {
				var reader io.Reader
				var file *os.File
				if op.After.ID != "" {
					file, e = s.openStage(ctx, id, op.After)
					if e != nil {
						return s.cleanupFailure(root, id, e)
					}
					reader = file
				}
				e = atomicStream(ctx, root, op.Path, op.Before.SHA256, reader, op.After.ID == "")
				if file != nil {
					file.Close()
				}
				if e != nil {
					return s.cleanupFailure(root, id, e)
				}
				if s.fault != nil {
					if e = s.fault("file"); e != nil {
						return e
					}
				}
			}
			if e = s.revalidateServer(ctx, id, root); e != nil {
				return s.cleanupFailure(root, id, e)
			}
			if e = participant.ValidatePostimages(ctx); e != nil {
				return s.cleanupFailure(root, id, e)
			}
			for _, op := range p.Operations {
				hash, _, e := currentHash(ctx, root, op.Path, MaxFileBytes)
				if e != nil || hash != op.After.SHA256 {
					return s.cleanupFailure(root, id, conflict("published pack postimage changed"))
				}
			}
			if s.fault != nil {
				if e = s.fault("before_commit"); e != nil {
					return s.cleanupFailure(root, id, e)
				}
			}
			tx, e := s.db.BeginTx(ctx, nil)
			if e != nil {
				return s.cleanupFailure(root, id, e)
			}
			defer tx.Rollback()
			if len(p.Request.Manual) > 0 || p.Request.ArchiveReceiptID != "" {
				var role domain.Role
				var disabled bool
				if err := tx.QueryRowContext(ctx, "SELECT role,disabled FROM users WHERE id=?", actor.ID).Scan(&role, &disabled); err != nil || disabled || role != domain.RoleAdmin {
					e = domain.E(domain.CodeForbidden, "administrator authority changed before pack commit")
				}
			}
			if e == nil {
				e = participant.Commit(ctx, tx)
			}
			if e == nil {
				baseline, _ := json.Marshal(p.Baseline)
				_, e = tx.ExecContext(ctx, "INSERT INTO modpack_baselines(instance_id,baseline_json) VALUES(?,?) ON CONFLICT(instance_id) DO UPDATE SET baseline_json=excluded.baseline_json", id, string(baseline))
			}
			if e == nil {
				_, e = tx.ExecContext(ctx, "UPDATE modpack_previews SET state='applied' WHERE id=?", p.Preview.ID)
			}
			if e == nil {
				_, e = tx.ExecContext(ctx, "UPDATE modpack_journals SET state='committed' WHERE instance_id=?", id)
			}
			if e != nil {
				tx.Rollback()
				return s.cleanupFailure(root, id, e)
			}
			if e = tx.Commit(); e != nil {
				return s.cleanupFailure(root, id, e)
			}
			if s.fault != nil {
				if e = s.fault("commit"); e != nil {
					return e
				}
			}
			return s.recoverJournal(ctx, root, id)
		})
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		outcome := "applied"
		if result != nil {
			outcome = "failed_or_recovery_required"
		}
		return errors.Join(result, s.audit(cleanup, id, actor, "apply", outcome))
	})
}
func (s *Service) cleanupFailure(root *os.Root, id string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(cause, s.recoverJournal(ctx, root, id))
}
func (s *Service) recoverJournal(ctx context.Context, root *os.Root, id string) error {
	var raw, state, preview string
	e := s.db.QueryRowContext(ctx, "SELECT private_json,state,preview_id FROM modpack_journals WHERE instance_id=?", id).Scan(&raw, &state, &preview)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var p privatePreview
	if json.Unmarshal([]byte(raw), &p) != nil || p.Preview.ID != preview || p.Preview.InstanceID != id || len(p.Operations) > MaxPackFiles {
		return conflict("pack journal corrupt")
	}
	committed := state == "committed"
	for _, op := range p.Operations {
		if !allowedPhysical(op.Path) {
			return conflict("pack journal path is outside owned boundary")
		}
		hash, _, e := currentHash(ctx, root, op.Path, MaxFileBytes)
		if e != nil {
			return e
		}
		if committed && hash != op.After.SHA256 || !committed && hash != op.Before.SHA256 && hash != op.After.SHA256 {
			return conflict("external pack edit preserved; recovery requires review")
		}
	}
	if s.fault != nil {
		if e = s.fault("recovery_preflight"); e != nil {
			return e
		}
	}
	if !committed {
		for i := len(p.Operations) - 1; i >= 0; i-- {
			op := p.Operations[i]
			hash, _, e := currentHash(ctx, root, op.Path, MaxFileBytes)
			if e != nil {
				return e
			}
			if hash == op.Before.SHA256 {
				continue
			}
			if hash != op.After.SHA256 {
				return conflict("external pack edit preserved during replay; recovery requires review")
			}
			var reader io.Reader
			var f *os.File
			if op.Before.ID != "" {
				f, e = s.openStage(ctx, id, op.Before)
				if e != nil {
					return e
				}
				reader = f
			}
			e = atomicStream(ctx, root, op.Path, op.After.SHA256, reader, op.Before.ID == "")
			if f != nil {
				f.Close()
			}
			if e != nil {
				return e
			}
		}
		if _, e = s.db.ExecContext(ctx, "UPDATE modpack_journals SET state='rolled_back' WHERE instance_id=?", id); e != nil {
			return e
		}
	}
	if e = s.revalidateServer(ctx, id, root); e != nil {
		return e
	}
	if e = s.clearTransaction(ctx, id, preview); e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM modpack_journals WHERE instance_id=?", id)
	return e
}
func allowedPhysical(p string) bool { return allowedPath(strings.TrimSuffix(p, ".disabled")) }

func (s *Service) PreviewNeedsAdministrator(ctx context.Context, id, preview string, actor int64) (bool, error) {
	p, e := s.loadPreview(ctx, id, preview, actor)
	return len(p.Request.Manual) > 0 || p.Request.ArchiveReceiptID != "", e
}
func (s *Service) RequestNeedsAdministrator(ctx context.Context, id string, r Request, actor int64) (bool, error) {
	if len(r.Manual) > 0 || r.ArchiveReceiptID != "" {
		return true, nil
	}
	if r.PreviousPreviewID != "" {
		return s.PreviewNeedsAdministrator(ctx, id, r.PreviousPreviewID, actor)
	}
	return false, nil
}
