package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type Service struct {
	languageEvidence func(context.Context, string, domain.VersionSelection) (loaderutil.LanguageEvidence, error)
	db               *sql.DB
	paths            instance.Paths
	instances        Instances
	queue            *jobs.Service
	catalog          Catalog
	recovery         RecoveryGuard
	mu               sync.Mutex
	uploads          chan struct{}
	fault            func(string) error
}

func New(c Config) (*Service, error) {
	if c.DB == nil || c.Instances == nil || c.Queue == nil {
		return nil, invalid("content requires database, instances and jobs")
	}
	return &Service{languageEvidence: c.LanguageEvidence, db: c.DB, paths: c.Paths, instances: c.Instances, queue: c.Queue, catalog: c.Catalog, recovery: c.Recovery, uploads: make(chan struct{}, 2)}, nil
}
func (s *Service) Register() error { return s.queue.Register("content_mutation", s.run) }
func (s *Service) actor(ctx context.Context, id int64, admin bool) (domain.User, error) {
	u := domain.User{ID: id}
	if e := s.db.QueryRowContext(ctx, "SELECT username,role,disabled FROM users WHERE id=?", id).Scan(&u.Username, &u.Role, &u.Disabled); e != nil || u.Disabled || !u.Role.AtLeast(domain.RoleOperator) || admin && u.Role != domain.RoleAdmin {
		return u, domain.E(domain.CodeForbidden, "current content authority is insufficient")
	}
	return u, nil
}
func (s *Service) Snapshot(ctx context.Context, id string) (Manifest, error) {
	if e := domain.ValidateInstanceID(id); e != nil {
		return Manifest{}, invalid("invalid instance id")
	}
	detail, e := s.instances.Get(ctx, id)
	if e != nil {
		return Manifest{}, e
	}
	m := Manifest{Tuple: tuple(detail.Instance), Items: []Installed{}, AutomaticBlockers: []string{}}
	rows, e := s.db.QueryContext(ctx, "SELECT manifest_json FROM content_items WHERE instance_id=? ORDER BY id", id)
	if e != nil {
		return m, e
	}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			break
		}
		var i Installed
		if e = json.Unmarshal([]byte(raw), &i); e != nil {
			break
		}
		m.Items = append(m.Items, i)
		if i.Unknown {
			m.AutomaticBlockers = append(m.AutomaticBlockers, "Unknown content requires review: "+i.ID)
		}
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return m, e
	}
	// Revalidate active/disabled owned bytes before exposing update eligibility.
	// A DB manifest is provenance, not evidence that local files are unchanged.
	root, openErr := s.openServer(ctx, id)
	if openErr != nil {
		m.AutomaticBlockers = append(m.AutomaticBlockers, "Installed content files could not be verified.")
	} else {
		owned := map[string]bool{}
		for _, item := range m.Items {
			for _, file := range item.Files {
				owned[strings.ToLower(file.Path)] = true
				hash, _, readErr := currentHash(ctx, root, file.Path, MaxArtifactBytes)
				if readErr != nil || hash != file.SHA256 {
					m.AutomaticBlockers = append(m.AutomaticBlockers, "Managed content has local changes or unsafe files: "+item.ID)
				}
			}
		}
		for _, area := range []string{"mods", "plugins"} {
			before, err := root.Lstat(area)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
				m.AutomaticBlockers = append(m.AutomaticBlockers, "Content directory is linked or unavailable: "+area)
				continue
			}
			directory, err := root.OpenRoot(area)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				m.AutomaticBlockers = append(m.AutomaticBlockers, "Content directory could not be verified: "+area)
				continue
			}
			opened, identityErr := directory.Stat(".")
			if identityErr != nil || !os.SameFile(before, opened) {
				directory.Close()
				m.AutomaticBlockers = append(m.AutomaticBlockers, "Content directory identity changed: "+area)
				continue
			}
			f, err := directory.Open(".")
			if err != nil {
				directory.Close()
				continue
			}
			entries, err := f.ReadDir(4097)
			f.Close()
			directory.Close()
			if err != nil && err != io.EOF {
				m.AutomaticBlockers = append(m.AutomaticBlockers, "Content directory listing is incomplete: "+area)
			}
			if len(entries) > 4096 {
				m.AutomaticBlockers = append(m.AutomaticBlockers, "Content directory exceeds verification entry limit.")
			}
			for _, entry := range entries {
				lower := strings.ToLower(area + "/" + entry.Name())
				if (strings.HasSuffix(lower, ".jar") || strings.HasSuffix(lower, ".jar.disabled")) && !owned[lower] {
					m.AutomaticBlockers = append(m.AutomaticBlockers, "Unmanaged executable content requires review: "+entry.Name())
				}
			}
		}
		root.Close()
	}
	if _, present, markerErr := s.readMarker(ctx, id); present || markerErr != nil {
		m.AutomaticBlockers = append(m.AutomaticBlockers, "Content boot blocker requires recovery or review.")
	}
	var pending int
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_journals WHERE instance_id=?", id).Scan(&pending); e != nil {
		return m, e
	}
	if pending > 0 {
		m.AutomaticBlockers = append(m.AutomaticBlockers, "Content recovery or transaction is pending.")
	}
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_previews WHERE instance_id=? AND state IN ('preview','queued') AND created_at>? AND (private_json LIKE '%\"manual\":%' OR private_json LIKE '%\"blockers\":[\"%' OR private_json LIKE '%\"unknown\":true%')", id, cutoff()).Scan(&pending); e != nil {
		return m, e
	}
	if pending > 0 {
		m.AutomaticBlockers = append(m.AutomaticBlockers, "Unresolved content preview or manual artifact resolution.")
	}
	m.AutomaticBlockers = append(m.AutomaticBlockers, enabledManifestBlockers(m.Tuple, m.Items)...)
	raw, _ := json.Marshal(struct {
		Tuple Tuple
		Items []Installed
	}{m.Tuple, m.Items})
	m.Revision = digest(raw)
	return m, nil
}
func (s *Service) List(ctx context.Context, id string) ([]Installed, error) {
	m, e := s.Snapshot(ctx, id)
	return m.Items, e
}
func cutoff() string { return time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano) }
func (s *Service) expire(ctx context.Context, id string) error {
	if held, err := s.stagesHeld(ctx, id, s.db); err != nil || held {
		return err
	}
	var n int
	if e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_journals WHERE instance_id=?", id).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return nil
	}
	if _, e := s.db.ExecContext(ctx, "DELETE FROM content_previews WHERE instance_id=? AND (created_at<? OR state='applied') AND NOT EXISTS(SELECT 1 FROM jobs WHERE jobs.instance_id=content_previews.instance_id AND kind='content_mutation' AND "+contentJobPreview+"=content_previews.id AND jobs.state IN ('queued','running'))", id, cutoff()); e != nil {
		return e
	}
	return s.cleanupStages(ctx, id, 0, true)
}
func (s *Service) cleanupStages(ctx context.Context, id string, actor int64, expired bool) error {
	// Keep queue/reference publication serialized through unlink + receipt deletion.
	// Production SQLite uses one connection; taking a write reservation also
	// preserves the boundary if a caller supplies a larger connection pool.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE content_stages SET id=id WHERE instance_id=? AND 0", id); err != nil {
		return err
	}
	if held, err := s.stagesHeld(ctx, id, tx); err != nil || held {
		return err
	}
	query := `SELECT id FROM content_stages WHERE instance_id=?
 AND NOT EXISTS(SELECT 1 FROM content_previews WHERE instance_id=? AND instr(private_json,content_stages.id)>0)
 AND NOT EXISTS(SELECT 1 FROM update_previews WHERE instance_id=content_stages.instance_id AND instr(private_json,content_stages.id)>0 AND (julianday(expires_at) IS NULL OR julianday(expires_at)>julianday('now') OR EXISTS(SELECT 1 FROM jobs WHERE jobs.instance_id=update_previews.instance_id AND jobs.state IN ('queued','running') AND jobs.kind IN ('instance_update_prepare','instance_update','instance_rollback') AND instr(jobs.payload,update_previews.id)>0)))
 AND NOT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=content_stages.instance_id AND instr(journal_json,content_stages.id)>0 AND (state NOT IN ('complete','rolled_back','superseded') OR EXISTS(SELECT 1 FROM jobs WHERE jobs.id=update_transactions.job_id AND jobs.state IN ('queued','running'))))
 AND NOT EXISTS(SELECT 1 FROM jobs WHERE instance_id=content_stages.instance_id AND state IN ('queued','running') AND kind IN ('instance_update_prepare','instance_update','instance_rollback') AND instr(payload,content_stages.id)>0)`
	args := []any{id, id}
	if actor > 0 {
		query += " AND actor_id=?"
		args = append(args, actor)
	}
	if expired {
		query += " AND created_at<?"
		args = append(args, cutoff())
	}
	rows, e := tx.QueryContext(ctx, query, args...)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var stage string
		if e = rows.Scan(&stage); e != nil {
			break
		}
		ids = append(ids, stage)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return e
	}
	if s.fault != nil {
		if err := s.fault("stage_cleanup"); err != nil {
			return err
		}
	}
	for _, stage := range ids {
		if e = s.removeStageFile(ctx, id, stage); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "DELETE FROM content_stages WHERE id=? AND instance_id=?", stage, id); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Service) Discard(ctx context.Context, id, preview string, actor domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, e := s.actor(ctx, actor.ID, false); e != nil {
		return e
	}
	var n int
	if e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM content_journals WHERE instance_id=?", id).Scan(&n); e != nil {
		return e
	}
	if n > 0 {
		return conflict("recover transaction before discarding stages")
	}
	if preview != "" {
		ownership, e := s.reconcilePreviewJob(ctx, id, preview)
		if e != nil {
			return e
		}
		if ownership.ActorID != actor.ID {
			return domain.E(domain.CodeForbidden, "content preview belongs to another actor")
		}
		if ownership.Live || ownership.Journal {
			return conflict("preview is owned by a live job or recovery journal")
		}
		result, e := s.db.ExecContext(ctx, "DELETE FROM content_previews WHERE id=? AND instance_id=? AND actor_id=? AND NOT EXISTS(SELECT 1 FROM jobs WHERE jobs.instance_id=content_previews.instance_id AND kind='content_mutation' AND "+contentJobPreview+"=content_previews.id AND jobs.state IN ('queued','running'))", preview, id, actor.ID)
		if e != nil {
			return e
		}
		count, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if count != 1 {
			return conflict("preview ownership changed before discard")
		}
		return nil
	}

	return s.cleanupStages(ctx, id, actor.ID, false)
}
func (s *Service) audit(ctx context.Context, id string, u domain.User, action, target, outcome string) error {
	b, _ := json.Marshal(map[string]string{"outcome": outcome})
	_, e := s.db.ExecContext(ctx, "INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), u.ID, u.Username, "content."+action, id, target, string(b))
	return e
}
