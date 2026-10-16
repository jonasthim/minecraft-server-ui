package modpack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"sync"
	"time"
)

const MaxArtifactBytes = MaxFileBytes
const MaxConfigBytes = 1 << 20

type Service struct {
	db        *sql.DB
	paths     instance.Paths
	instances Instances
	queue     *jobs.Service
	catalog   Catalog
	content   ContentService
	recovery  Recovery
	mu        sync.Mutex
	fault     func(string) error
}

func New(c Config) (*Service, error) {
	if c.DB == nil || c.Instances == nil || c.Queue == nil || c.Catalog == nil || c.Content == nil {
		return nil, invalid("pack requires database, instances, jobs, catalog and content")
	}
	var synchronous int
	if e := c.DB.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous); e != nil {
		return nil, e
	}
	if synchronous < 2 {
		return nil, invalid("pack transactions require SQLite FULL synchronous durability")
	}
	return &Service{db: c.DB, paths: c.Paths, instances: c.Instances, queue: c.Queue, catalog: c.Catalog, content: c.Content, recovery: c.Recovery}, nil
}
func (s *Service) Register() error { return s.queue.Register("modpack_apply", s.run) }
func (s *Service) actor(ctx context.Context, id int64, admin bool) (domain.User, error) {
	u := domain.User{ID: id}
	if e := s.db.QueryRowContext(ctx, "SELECT username,role,disabled FROM users WHERE id=?", id).Scan(&u.Username, &u.Role, &u.Disabled); e != nil || u.Disabled || !u.Role.AtLeast(domain.RoleOperator) || admin && u.Role != domain.RoleAdmin {
		return u, domain.E(domain.CodeForbidden, "current pack authority insufficient")
	}
	return u, nil
}
func (s *Service) Snapshot(ctx context.Context, id string) (Snapshot, error) {
	if e := domain.ValidateInstanceID(id); e != nil {
		return Snapshot{}, invalid("invalid instance")
	}
	if _, e := s.instances.Get(ctx, id); e != nil {
		return Snapshot{}, e
	}
	out := Snapshot{Blockers: []string{}}
	var raw string
	e := s.db.QueryRowContext(ctx, "SELECT baseline_json FROM modpack_baselines WHERE instance_id=?", id).Scan(&raw)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if e == nil {
		var b Baseline
		if e = json.Unmarshal([]byte(raw), &b); e != nil {
			return out, conflict("pack baseline corrupt")
		}
		out.Baseline = &b
	}
	if e = s.CheckReady(ctx, id); e != nil {
		out.Blockers = append(out.Blockers, "Pack transaction or recovery requires review")
	}
	var unresolved int
	if e = s.db.QueryRowContext(ctx, "SELECT count(*) FROM modpack_previews WHERE instance_id=? AND state IN ('preview','queued') AND created_at>? AND (private_json LIKE '%\"blockers\":[\"%' OR private_json LIKE '%\"manual\":[{%')", id, time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano)).Scan(&unresolved); e != nil {
		return out, e
	}
	if unresolved > 0 {
		out.Blockers = append(out.Blockers, "Unresolved pack preview or required manual member blocks automatic updates")
	}
	return out, nil
}
func (s *Service) audit(ctx context.Context, id string, u domain.User, action, outcome string) error {
	raw, _ := json.Marshal(map[string]string{"outcome": outcome})
	_, e := s.db.ExecContext(ctx, "INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,?,?)", time.Now().UTC().Format(time.RFC3339Nano), u.ID, u.Username, "modpack."+action, id, "pack", string(raw))
	return e
}
