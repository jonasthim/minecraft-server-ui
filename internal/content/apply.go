package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	e := s.db.QueryRowContext(ctx, "SELECT actor_id,private_json,state,created_at FROM content_previews WHERE instance_id=? AND id=?", id, preview).Scan(&owner, &raw, &state, &created)
	if errors.Is(e, sql.ErrNoRows) {
		return p, domain.NotFound("content preview")
	}
	if e != nil {
		return p, e
	}
	if owner != actor {
		return p, domain.E(domain.CodeForbidden, "content preview belongs to another actor")
	}
	if created < cutoff() {
		return p, conflict("content preview expired")
	}
	if state != "preview" && state != "queued" {
		return p, conflict("content preview has already been applied")
	}
	if json.Unmarshal([]byte(raw), &p) != nil || p.Preview.ID != preview || p.Preview.InstanceID != id || p.ActorID != owner {
		return p, conflict("private content preview is corrupt")
	}
	return p, nil
}
func (s *Service) Enqueue(ctx context.Context, id, preview string, actor domain.User) (jobs.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.CheckReady(ctx, id); e != nil {
		return jobs.Job{}, e
	}
	p, e := s.loadPreview(ctx, id, preview, actor.ID)
	if e != nil {
		return jobs.Job{}, e
	}
	if _, e = s.actor(ctx, actor.ID, needsAdmin(p.Mutation)); e != nil {
		return jobs.Job{}, e
	}
	if len(p.Preview.Blockers) > 0 || p.Preview.Manual != nil {
		return jobs.Job{}, conflict("resolve preview blockers before applying")
	}
	ownership, e := s.reconcilePreviewJob(ctx, id, preview)
	if e != nil {
		return jobs.Job{}, e
	}
	if ownership.Live || ownership.Journal {
		return jobs.Job{}, conflict("preview is owned by a live job or recovery journal")
	}
	payload, _ := json.Marshal(struct {
		PreviewID string `json:"preview_id"`
		ActorID   int64  `json:"actor_id"`
	}{preview, actor.ID})
	j, e := s.queue.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "content_mutation", Payload: payload, RequestedBy: actor.ID, Cancellable: true})
	if e != nil {
		return j, e
	}
	if s.fault != nil {
		if fault := s.fault("enqueue"); fault != nil {
			return j, fault
		}
	}
	// The durable job payload already owns the preview. A cancelled request or
	// manager crash here is repaired from that job; never reset it speculatively.
	result, bindErr := s.db.ExecContext(ctx, "UPDATE content_previews SET state='queued',job_id=? WHERE id=? AND state='preview'", j.ID, preview)
	if bindErr != nil {
		return j, bindErr
	}
	changed, bindErr := result.RowsAffected()
	if bindErr != nil {
		return j, bindErr
	}
	if changed != 1 {
		return j, conflict("durable content job admitted; refresh its preview ownership before retrying")
	}

	return j, e
}
func (s *Service) run(ctx context.Context, execution *jobs.Execution) (result error) {
	var request struct {
		PreviewID string `json:"preview_id"`
		ActorID   int64  `json:"actor_id"`
	}
	if json.Unmarshal(execution.Payload, &request) != nil || request.ActorID <= 0 {
		return invalid("invalid private content job")
	}
	return s.instances.WithStoppedJob(ctx, execution, func(session *instance.StoppedSession) (result error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		p, e := s.loadPreview(ctx, execution.Job.InstanceID, request.PreviewID, request.ActorID)
		if e != nil {
			return e
		}
		if e := s.CheckReady(ctx, p.Preview.InstanceID); e != nil {
			return e
		}
		ownership, e := s.reconcilePreviewJob(ctx, p.Preview.InstanceID, p.Preview.ID)
		if e != nil {
			return e
		}
		if !ownership.Live || ownership.JobID != execution.Job.ID {
			return conflict("current durable job does not own this preview")
		}
		actor, e := s.actor(ctx, request.ActorID, needsAdmin(p.Mutation))
		if e != nil {
			return e
		}
		detail, e := session.Detail(ctx)
		if e != nil {
			return e
		}
		if (p.Mutation.Action == "install" || p.Mutation.Action == "update" || p.Mutation.Action == "enable") && detail.Provisioning != "installed" {
			return conflict("server installation changed before content activation")
		}
		id := detail.Instance.ID
		if id != p.Preview.InstanceID {
			return conflict("job instance differs from content preview")
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			outcome := "applied"
			if result != nil {
				outcome = "failed_or_conflicted"
			}
			result = errors.Join(result, s.audit(cleanup, id, actor, p.Mutation.Action, func() string {
				if p.Mutation.ConfigPath != "" {
					return p.Mutation.ConfigPath
				}
				return p.Mutation.ContentID
			}(), outcome))
			if result != nil {
				_ = execution.Log(cleanup, "Content operation failed or conflicted. Local files and durable recovery state require review before retrying.")
			}
		}()
		if e = execution.Phase(ctx, "verify_content", 15); e != nil {
			return e
		}
		m, e := s.Snapshot(ctx, id)
		if e != nil {
			return e
		}
		if m.Revision != p.Preview.Revision || tuple(detail.Instance) != p.Preview.Tuple {
			return conflict("content manifest or server tuple changed since preview")
		}
		if len(p.Preview.Blockers) > 0 || p.Preview.Manual != nil {
			return conflict("preview has unresolved blockers")
		}
		if len(proposedEnabledBlockers(m.Tuple, m.Items, p)) > 0 {
			return conflict("proposed enabled content has unresolved compatibility contradictions")
		}
		if p.Mutation.Selection != nil {
			v, e := s.catalog.Version(ctx, p.Mutation.Selection.Provider, p.Mutation.Selection.ProjectID, p.Mutation.Selection.VersionID)
			if e != nil {
				return domain.E(domain.CodeUpstreamError, "publisher metadata could not be revalidated")
			}
			if metadataDigest(v) != p.Metadata {
				return conflict("publisher metadata changed; create a fresh preview")
			}
		}
		root, e := s.openServer(ctx, id)
		if e != nil {
			return e
		}
		defer root.Close()
		for _, op := range p.Operations {
			hash, _, e := currentHash(ctx, root, op.Path, MaxArtifactBytes)
			if e != nil {
				return e
			}
			if hash != op.Before {
				return conflict("local content changed since preview")
			}
			for _, stage := range []string{op.BeforeStage, op.AfterStage} {
				if stage != "" {
					_, f, e := s.openStage(ctx, id, stage)
					if e != nil {
						return e
					}
					f.Close()
				}
			}
		}
		if _, e = s.actor(ctx, actor.ID, needsAdmin(p.Mutation)); e != nil {
			return e
		}
		raw, _ := json.Marshal(p)
		if _, e = s.db.ExecContext(ctx, "INSERT INTO content_journals(instance_id,preview_id,private_json,state) VALUES(?,?,?,'applying')", id, p.Preview.ID, string(raw)); e != nil {
			return conflict("another content transaction requires recovery")
		}
		if e = s.markTransaction(ctx, id, p.Preview.ID); e != nil {
			return s.cleanupFailure(root, id, e)
		}
		if e = execution.Phase(ctx, "publish_content", 40); e != nil {
			return s.cleanupFailure(root, id, e)
		}
		if s.fault != nil {
			if e = s.fault("journal"); e != nil {
				return s.cleanupFailure(root, id, e)
			}
		}
		for _, op := range p.Operations {
			var reader io.Reader
			var stage *os.File
			if op.After != "" {
				_, stage, e = s.openStage(ctx, id, op.AfterStage)
				if e != nil {
					return s.cleanupFailure(root, id, e)
				}
				reader = stage
			}
			e = atomicStream(ctx, root, op.Path, op.Before, reader, op.After == "")
			if stage != nil {
				stage.Close()
			}
			if e != nil {
				return s.cleanupFailure(root, id, e)
			}
			hash, _, e := currentHash(ctx, root, op.Path, MaxArtifactBytes)
			if e != nil || hash != op.After {
				return s.cleanupFailure(root, id, conflict("content publication readback differs"))
			}
			if s.fault != nil {
				if e = s.fault("file"); e != nil {
					return s.cleanupFailure(root, id, e)
				}
			}
		}
		if e = s.revalidateServer(ctx, id, root); e != nil {
			return s.cleanupFailure(root, id, e)
		}
		if _, e = s.actor(ctx, actor.ID, needsAdmin(p.Mutation)); e != nil {
			return s.cleanupFailure(root, id, e)
		}
		if e = ctx.Err(); e != nil {
			return s.cleanupFailure(root, id, e)
		}
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return s.cleanupFailure(root, id, e)
		}
		defer tx.Rollback()
		if p.Next != nil {
			manifest, _ := json.Marshal(p.Next)
			_, e = tx.ExecContext(ctx, "INSERT INTO content_items(instance_id,id,manifest_json) VALUES(?,?,?) ON CONFLICT(instance_id,id) DO UPDATE SET manifest_json=excluded.manifest_json", id, p.Next.ID, string(manifest))
		} else if p.Mutation.Action == "uninstall" {
			_, e = tx.ExecContext(ctx, "DELETE FROM content_items WHERE instance_id=? AND id=?", id, p.Mutation.ContentID)
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, "UPDATE content_previews SET state='applied' WHERE id=?", p.Preview.ID)
		}
		if e == nil {
			_, e = tx.ExecContext(ctx, "UPDATE content_journals SET state='committed' WHERE instance_id=?", id)
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
		if e = s.recoverJournal(ctx, root, id); e != nil {
			return e
		}
		return execution.Log(ctx, "Stopped content transaction applied and read back. Addon configuration was preserved; game compatibility remains unqualified.")
	})
}
func (s *Service) cleanupFailure(root *os.Root, id string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(cause, s.recoverJournal(ctx, root, id))
}
func (s *Service) recoverJournal(ctx context.Context, root *os.Root, id string) error {
	var raw, state string
	e := s.db.QueryRowContext(ctx, "SELECT private_json,state FROM content_journals WHERE instance_id=?", id).Scan(&raw, &state)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var p privatePreview
	if json.Unmarshal([]byte(raw), &p) != nil || p.Preview.InstanceID != id {
		return conflict("content recovery journal is corrupt")
	}
	if e = s.markTransaction(ctx, id, p.Preview.ID); e != nil {
		return e
	}
	for i := len(p.Operations) - 1; i >= 0; i-- {
		op := p.Operations[i]
		if !relative(op.Path) || op.Path == "server.properties" || !strings.HasPrefix(op.Path, "mods/") && !strings.HasPrefix(op.Path, "plugins/") && !configPath(op.Path) {
			return conflict("content recovery target outside owned scope")
		}
		hash, _, e := currentHash(ctx, root, op.Path, MaxArtifactBytes)
		if e != nil {
			return s.blockRecovery(ctx, id, e)
		}
		if state == "rolled_back" {
			if hash != op.Before {
				return s.blockRecovery(ctx, id, conflict("rolled-back content changed before reconciliation"))
			}
			continue
		}
		if state == "committed" {
			if hash != op.After {
				return s.blockRecovery(ctx, id, conflict("committed content changed before reconciliation"))
			}
			continue
		}
		if hash == op.Before {
			continue
		}
		if hash != op.After {
			return s.blockRecovery(ctx, id, conflict("recovery preserved a competing local change"))
		}
		var f *os.File
		var reader io.Reader
		if op.Before != "" {
			_, f, e = s.openStage(ctx, id, op.BeforeStage)
			if e != nil {
				return s.blockRecovery(ctx, id, e)
			}
			reader = f
		}
		e = atomicStream(ctx, root, op.Path, hash, reader, op.Before == "")
		if f != nil {
			f.Close()
		}
		if e != nil {
			return s.blockRecovery(ctx, id, e)
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if state != "committed" {
		_, e = tx.ExecContext(ctx, "UPDATE content_previews SET state=CASE WHEN EXISTS(SELECT 1 FROM jobs WHERE jobs.instance_id=content_previews.instance_id AND kind='content_mutation' AND "+contentJobPreview+"=content_previews.id AND jobs.state IN ('queued','running')) THEN 'queued' ELSE 'preview' END WHERE id=?", p.Preview.ID)
	}
	if e == nil && state != "committed" {
		_, e = tx.ExecContext(ctx, "UPDATE content_journals SET state='rolled_back' WHERE instance_id=?", id)
	}
	if e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	// SQL records a verified terminal outcome before its independent boot blocker
	// is removed. A crash after marker removal can only leave consistent files.
	if e = s.revalidateServer(ctx, id, root); e != nil {
		return e
	}
	if e = s.clearTransaction(ctx, id, p.Preview.ID); e != nil {
		return e
	}
	_, e = s.db.ExecContext(ctx, "DELETE FROM content_journals WHERE instance_id=?", id)
	return e
}
func (s *Service) blockRecovery(ctx context.Context, id string, cause error) error {
	_, e := s.db.ExecContext(ctx, "UPDATE content_journals SET state='blocked' WHERE instance_id=? AND state NOT IN ('committed','rolled_back')", id)
	return errors.Join(cause, e)
}

// Reconcile runs after jobs.StartPaused and before native starts / jobs.Resume.
func (s *Service) Reconcile(ctx context.Context) error {
	if s.recovery == nil {
		return domain.E(domain.CodeUnavailable, "content startup recovery admission unavailable")
	}
	instanceRows, err := s.db.QueryContext(ctx, "SELECT id FROM instances ORDER BY id")
	if err != nil {
		return err
	}
	all := []string{}
	for instanceRows.Next() {
		var id string
		if err = instanceRows.Scan(&id); err != nil {
			break
		}
		all = append(all, id)
	}
	err = errors.Join(err, instanceRows.Err(), instanceRows.Close())
	if err != nil {
		return err
	}
	for _, id := range all {
		if err = s.verifyMarkerJournal(ctx, id); err != nil {
			return err
		}
	}
	rows, e := s.db.QueryContext(ctx, "SELECT instance_id FROM content_journals ORDER BY instance_id")
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.recovery.WithStoppedForRecovery(ctx, id, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			root, e := s.openServer(ctx, id)
			if e != nil {
				return e
			}
			defer root.Close()
			return s.recoverJournal(ctx, root, id)
		}); e != nil {
			return e
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range all {
		if e = s.reconcileInstancePreviewJobs(ctx, id); e != nil {
			return e
		}
	}
	return nil
}

// PreviewNeedsAdministrator lets the API apply the same token scope as the private action.
func (s *Service) PreviewNeedsAdministrator(ctx context.Context, id, preview string, actorID int64) (bool, error) {
	p, e := s.loadPreview(ctx, id, preview, actorID)
	return needsAdmin(p.Mutation), e
}
