// Package players preserves bounded native player observations and operates
// lists through the existing instance job admission and console transport.
package players

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"os"
	"sync"
	"time"
)

type Native interface {
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
	WithStopped(context.Context, string, func() error) error
}
type Service struct {
	db        *sql.DB
	paths     instance.Paths
	instances *instance.Service
	console   *console.Service
	queue     *jobs.Service
	native    Native
	mu        sync.Mutex
}
type Mutation struct {
	Mode string `json:"mode"`
	console.PlayerMutation
}
type request struct {
	Mutation
	ActorID int64 `json:"actor_id"`
}

func New(db *sql.DB, paths instance.Paths, instances *instance.Service, controls *console.Service, queue *jobs.Service, native Native) *Service {
	return &Service{db: db, paths: paths, instances: instances, console: controls, queue: queue, native: native}
}
func (s *Service) Register() error {
	if s.queue == nil {
		return domain.E(domain.CodeUnavailable, "player queue unavailable")
	}
	return s.queue.Register("player_mutation", s.runMutation)
}
func authorize(actor domain.User, m Mutation) error {
	if actor.ID <= 0 || actor.Disabled || !actor.Role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "active operator access required")
	}
	if (m.Action == "op" || m.Action == "deop") && actor.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "administrator access required for operator privileges")
	}
	if err := console.ValidatePlayerMutation(m.PlayerMutation); err != nil {
		return err
	}
	if m.Mode != "online" && m.Mode != "offline" {
		return domain.E(domain.CodeValidationFailed, "choose online or offline operation explicitly")
	}
	if m.Mode == "offline" && (m.Action == "kick" || m.UUID == "") {
		return domain.E(domain.CodeValidationFailed, "offline list changes require UUID; kicking requires an online server")
	}
	return nil
}
func (s *Service) Enqueue(ctx context.Context, id string, m Mutation, actor domain.User) (jobs.Job, error) {
	if e := authorize(actor, m); e != nil {
		return jobs.Job{}, e
	}
	if s.instances == nil || s.queue == nil {
		return jobs.Job{}, domain.E(domain.CodeUnavailable, "player service unavailable")
	}
	if _, e := s.instances.Get(ctx, id); e != nil {
		return jobs.Job{}, e
	}
	if e := s.audit(ctx, id, actor, "request", m, "requested"); e != nil {
		return jobs.Job{}, e
	}
	b, e := json.Marshal(request{m, actor.ID})
	if e != nil {
		return jobs.Job{}, e
	}
	return s.queue.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "player_mutation", Payload: b, RequestedBy: actor.ID, Cancellable: true})
}
func (s *Service) audit(ctx context.Context, id string, actor domain.User, phase string, m Mutation, outcome string) error {
	details, _ := json.Marshal(map[string]string{"outcome": outcome, "action": m.Action, "mode": m.Mode})
	_, e := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor.ID, actor.Username, "players."+phase, id, func() string {
		if m.UUID != "" {
			return m.UUID
		}
		return m.Name
	}(), string(details))
	if e != nil {
		return fmt.Errorf("audit player operation: %w", e)
	}
	return nil
}
func (s *Service) runMutation(ctx context.Context, e *jobs.Execution) (result error) {
	var req request
	if json.Unmarshal(e.Payload, &req) != nil || req.ActorID <= 0 {
		return domain.E(domain.CodeValidationFailed, "invalid private player job")
	}
	actor := domain.User{ID: req.ActorID}
	if err := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, actor.ID).Scan(&actor.Username, &actor.Role, &actor.Disabled); err != nil {
		return domain.E(domain.CodeForbidden, "player operation requester unavailable")
	}
	defer func() {
		outcome := "verified"
		if result != nil {
			outcome = "failed_or_partial"
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if result != nil {
			safe := domain.AsError(result)
			message := safe.Message
			if safe.Code == domain.CodeInternal {
				message = "Player operation failed; changes may be partial. Inspect native state and saved lists before retrying."
			}
			result = errors.Join(result, e.Log(cleanup, message))
		}
		result = errors.Join(result, s.audit(cleanup, e.Job.InstanceID, actor, "result", req.Mutation, outcome))
	}()
	if err := authorize(actor, req.Mutation); err != nil {
		return err
	}
	if err := e.Phase(ctx, "verifying_player_operation", 20); err != nil {
		return err
	}
	// Explicit mode picks exactly one admission. An uncertain online state never
	// falls through to stopped file edits.
	if req.Mode == "offline" {
		return s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
			dirs, err := session.Paths(ctx)
			if err != nil {
				return err
			}
			root, err := os.OpenRoot(dirs.Server)
			if err != nil {
				return err
			}
			defer root.Close()
			if err = mutateSaved(ctx, root, req.PlayerMutation, time.Now()); err != nil {
				return err
			}
			list, err := savedList(ctx, root, mutationKind(req.Action))
			if err != nil {
				return domain.E(domain.CodeUnavailable, "list change may have applied; saved readback unavailable")
			}
			if err = verifyMembership(list, req.PlayerMutation); err != nil {
				return err
			}
			return e.Log(ctx, "Stopped native player list changed and read back. Runtime state is unverified until the next start.")
		})
	}
	if s.console == nil {
		return domain.E(domain.CodeUnavailable, "native player console unavailable")
	}
	return s.console.WithJobSession(ctx, e, func(session *console.Session) error {
		ack, err := session.MutatePlayer(ctx, req.PlayerMutation)
		if err != nil {
			if ack.Received {
				return domain.E(domain.CodeUnavailable, "operation may have been delivered; acknowledgement or readback unavailable; inspect before retrying")
			}
			return err
		}
		if !ack.Acknowledged {
			return domain.E(domain.CodeUnavailable, "operation delivered without native acknowledgement")
		}
		if req.UUID != "" && !ack.ReadbackVerified {
			return domain.E(domain.CodeUnavailable, "native mutation acknowledged without independent readback")
		}
		if req.UUID == "" {
			if req.Action == "kick" {
				snapshot, err := session.PlayerSnapshot(ctx)
				if err != nil {
					return domain.E(domain.CodeUnavailable, "kick acknowledged; roster readback unavailable")
				}
				for _, p := range snapshot.Players {
					if p.Name == req.Name {
						return domain.E(domain.CodeUnavailable, "kick acknowledged but username remains in roster")
					}
				}
			} else {
				identity, err := session.Identity(ctx)
				if err != nil {
					return err
				}
				dirs, err := s.paths.For(identity.InstanceID)
				if err != nil {
					return err
				}
				root, err := os.OpenRoot(dirs.Server)
				if err != nil {
					return domain.E(domain.CodeUnavailable, "operation acknowledged; saved list readback unavailable")
				}
				defer root.Close()
				list, err := savedList(ctx, root, mutationKind(req.Action))
				if err != nil {
					return domain.E(domain.CodeUnavailable, "operation acknowledged; saved list readback unavailable")
				}
				if err = verifyMembership(list, req.PlayerMutation); err != nil {
					return err
				}
			}
		}
		return e.Log(ctx, "Native player operation acknowledged and read back; saved JSON does not prove plugin runtime overrides.")
	})
}
func verifyMembership(list console.PlayerList, m console.PlayerMutation) error {
	found := false
	for _, entry := range list.Entries {
		match := m.UUID != "" && entry.Player.UUID == m.UUID || m.UUID == "" && entry.Player.Name == m.Name
		if match {
			found = true
			if m.Action == "op" && m.PermissionLevel != nil && (entry.PermissionLevel == nil || *entry.PermissionLevel != *m.PermissionLevel) {
				return domain.E(domain.CodeUnavailable, "operation may have applied; permission readback differs")
			}
		}
	}
	want := m.Action == "whitelist_add" || m.Action == "ban" || m.Action == "op"
	if found != want {
		return domain.E(domain.CodeUnavailable, "operation may have applied; saved list readback differs")
	}
	return nil
}
func (s *Service) Lists(ctx context.Context, id, kind string) (console.PlayerList, error) {
	if _, err := filename(kind); err != nil {
		return console.PlayerList{}, err
	}
	if s.instances == nil || s.queue == nil {
		return console.PlayerList{}, domain.E(domain.CodeUnavailable, "player service unavailable")
	}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return console.PlayerList{}, err
	}
	if s.console != nil {
		list, err := s.console.NativePlayerList(ctx, id, kind)
		if err == nil {
			return list, nil
		}
		if domain.AsError(err).Code == domain.CodeInstanceBusy {
			return list, err
		}
	}
	var list console.PlayerList
	err := s.queue.WithIdle(ctx, id, func() error {
		dirs, err := s.paths.For(id)
		if err != nil {
			return err
		}
		var before domain.ProcessIdentity
		if s.native != nil {
			before, err = s.native.Identity(ctx, id)
			if err != nil && !os.IsNotExist(err) {
				return domain.E(domain.CodeUnavailable, "native generation unavailable for saved list snapshot")
			}
		}
		root, err := os.OpenRoot(dirs.Server)
		if err != nil {
			return domain.E(domain.CodeUnavailable, "saved list directory unavailable")
		}
		defer root.Close()
		list, err = savedList(ctx, root, kind)
		if err != nil {
			return err
		}
		if s.native != nil {
			after, e := s.native.Identity(ctx, id)
			if e != nil && !os.IsNotExist(e) || after != before {
				return domain.E(domain.CodeUnavailable, "native generation changed while reading saved list")
			}
		}
		return nil
	})
	return list, err
}
func (s *Service) Snapshot(ctx context.Context, id string) (console.PlayerSnapshot, error) {
	if err := domain.ValidateInstanceID(id); err != nil {
		return console.PlayerSnapshot{}, domain.E(domain.CodeValidationFailed, "invalid instance id")
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM player_snapshots WHERE instance_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		if s.instances != nil {
			if _, e := s.instances.Get(ctx, id); e != nil {
				return console.PlayerSnapshot{}, e
			}
		}
		return console.PlayerSnapshot{InstanceID: id, Players: []console.PlayerIdentity{}, Gap: true, Reason: "Player observation has not started."}, nil
	}
	if err != nil {
		return console.PlayerSnapshot{}, fmt.Errorf("read player snapshot: %w", err)
	}
	var snap console.PlayerSnapshot
	if json.Unmarshal([]byte(raw), &snap) != nil {
		return snap, domain.E(domain.CodeUnavailable, "stored player snapshot invalid")
	}
	if time.Since(snap.ObservedAt) > 45*time.Second {
		snap.Known = false
		snap.Complete = false
		snap.Gap = true
		snap.Reason = "Player observation is stale; last roster is historical."
	}
	return snap, nil
}
func (s *Service) Poll(ctx context.Context, id string) error {
	if s.console == nil {
		return domain.E(domain.CodeUnavailable, "player console unavailable")
	}
	snapshot, err := s.console.PlayerSnapshot(ctx, id)
	if err != nil {
		if domain.AsError(err).Code == domain.CodeInstanceBusy {
			return err
		}
		snapshot = console.PlayerSnapshot{InstanceID: id, ObservedAt: time.Now().UTC(), Players: []console.PlayerIdentity{}, Gap: true, Reason: "Native roster observation unavailable; presence and transitions are unknown."}
	}
	if e := s.record(ctx, snapshot); e != nil {
		return e
	}
	if e := s.consumeLogs(ctx, id); e != nil {
		return e
	}
	return err
}
func (s *Service) Run(ctx context.Context) error {
	if s.db == nil || s.console == nil {
		return domain.E(domain.CodeUnavailable, "player sampling unavailable")
	}
	work := make(chan string, 2)
	var workers sync.WaitGroup
	var mu sync.Mutex
	pending := map[string]bool{}
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range work {
				_ = s.Poll(ctx, id)
				mu.Lock()
				delete(pending, id)
				mu.Unlock()
			}
		}()
	}
	defer func() { close(work); workers.Wait() }()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		rows, err := s.db.QueryContext(ctx, `SELECT id FROM instances ORDER BY id`)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		ids := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				break
			}
			ids = append(ids, id)
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return err
		}
		for _, id := range ids {
			mu.Lock()
			active := pending[id]
			if !active {
				pending[id] = true
			}
			mu.Unlock()
			if active {
				continue
			}
			select {
			case work <- id:
			case <-ctx.Done():
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
