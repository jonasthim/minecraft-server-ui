package instance

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
)

// SelectionParticipant contributes only manager-owned selection/control state to
// the coordinator transaction. Private control credentials never enter history.
type SelectionParticipant struct {
	owner      *SelectionParticipant
	scope      *TransactionScope
	active     atomic.Bool
	mu         sync.Mutex
	before     Detail
	revision   string
	target     domain.VersionSelection
	runtime    domain.JavaRuntime
	actorID    int64
	management *ManagementConfig
	listener   net.Listener
	committed  bool
}

// WithSelectionParticipant precomputes the private protocol transition and holds
// an OS listener for a new management port until the participant callback ends.
// Existing private credentials remain private and survive protocol upgrades.
func (s *TransactionScope) WithSelectionParticipant(ctx context.Context, revision string, target domain.VersionSelection, runtime domain.JavaRuntime, actor domain.User, fn func(*SelectionParticipant) error) (result error) {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "selection participant callback required")
	}
	if actor.ID <= 0 || actor.Disabled || !actor.Role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "operator selection authority required")
	}
	requirements, err := managedruntime.Select(target)
	if err != nil {
		return domain.Wrap(domain.CodeUnsupported, "target selection has no reviewed runtime rules", err)
	}
	if _, err = SettingsCapabilities(target); err != nil {
		return err
	}
	if runtime.ID == "" || runtime.Major != requirements.JavaMajor {
		return domain.E(domain.CodeConflict, "managed runtime differs from exact target requirements")
	}
	p := &SelectionParticipant{scope: s, revision: revision, target: target, runtime: runtime, actorID: actor.ID}
	p.owner = p
	defer func() {
		if p.listener != nil {
			result = errors.Join(result, p.listener.Close())
		}
	}()
	err = s.WithUse(ctx, func(operation context.Context, snapshot ScopeSnapshot) error {
		p.before = snapshot.Detail
		if revision != snapshot.Detail.Instance.UpdatedAt.Format(time.RFC3339Nano) {
			return domain.E(domain.CodeConflict, "instance revision changed before update")
		}
		desired := snapshot.Detail.Instance
		desired.Flavor = target.Flavor
		desired.MinecraftVersion = target.MinecraftVersion
		desired.LoaderVersion = target.LoaderVersion
		desired.JavaMajor = runtime.Major
		desired.JavaRuntimeID = ""
		if err := validateDraft(Draft{Instance: desired, Config: snapshot.Detail.Config}); err != nil {
			return err
		}
		previous, err := s.state.service.optionalManagement(operation, desired.ID)
		if err != nil {
			return err
		}
		protocol, modernErr := ManagementProtocol(target.MinecraftVersion)
		if modernErr != nil {
			return nil
		}
		if previous != nil {
			copy := *previous
			copy.Protocol = protocol
			p.management = &copy
			return nil
		}
		secret, err := managementSecret()
		if err != nil {
			return err
		}
		for attempt := 0; attempt < 64; attempt++ {
			listener, err := (&net.ListenConfig{}).Listen(operation, "tcp4", "127.0.0.1:0")
			if err != nil {
				return domain.E(domain.CodeUnavailable, "private update management port unavailable")
			}
			port := listener.Addr().(*net.TCPAddr).Port
			var used bool
			err = s.state.service.db.QueryRowContext(operation, "SELECT EXISTS(SELECT 1 FROM instance_tcp_ports WHERE port=?)", port).Scan(&used)
			if err != nil {
				listener.Close()
				return err
			}
			if used {
				listener.Close()
				continue
			}
			p.listener = listener
			p.management = &ManagementConfig{InstanceID: desired.ID, Host: "127.0.0.1", Port: port, Secret: secret, Protocol: protocol}
			return nil
		}
		return domain.E(domain.CodePortInUse, "no private update management port available")
	})
	if err != nil {
		return err
	}
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *SelectionParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	if p == nil || p.owner != p || !p.active.Load() {
		return domain.E(domain.CodeForbidden, "selection participant forged or expired")
	}
	if !p.mu.TryLock() {
		return domain.E(domain.CodeInstanceBusy, "selection participant overlaps")
	}
	defer p.mu.Unlock()
	if !p.active.Load() || p.committed {
		return domain.E(domain.CodeForbidden, "selection participant expired or consumed")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, snapshot ScopeSnapshot) error {
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", p.actorID).Scan(&role, &disabled); err != nil || disabled || !role.AtLeast(domain.RoleOperator) {
			return domain.E(domain.CodeForbidden, "selection authority changed before commit")
		}
		result, err := tx.ExecContext(operation, `UPDATE instances SET flavor=?,minecraft_version=?,loader_version=?,java_major=?,java_runtime_id=?,updated_at=? WHERE id=? AND updated_at=? AND flavor=? AND minecraft_version=? AND loader_version=?`, p.target.Flavor, p.target.MinecraftVersion, p.target.LoaderVersion, p.runtime.Major, p.runtime.ID, time.Now().UTC().Format(time.RFC3339Nano), p.before.Instance.ID, p.revision, p.before.Instance.Flavor, p.before.Instance.MinecraftVersion, p.before.Instance.LoaderVersion)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return domain.E(domain.CodeConflict, "selection changed before update commit")
		}
		id := snapshot.Detail.Instance.ID
		if p.management == nil {
			if _, err = tx.ExecContext(operation, "DELETE FROM instance_management WHERE instance_id=?", id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(operation, "DELETE FROM instance_tcp_ports WHERE instance_id=? AND kind='management'", id); err != nil {
				return err
			}
		} else {
			cfg := p.management
			if p.listener != nil {
				if _, err = tx.ExecContext(operation, "INSERT INTO instance_tcp_ports(port,instance_id,kind) VALUES(?,?,'management')", cfg.Port, id); err != nil {
					return domain.E(domain.CodePortInUse, "private management reservation changed before update commit")
				}
			}
			_, err = tx.ExecContext(operation, `INSERT INTO instance_management(instance_id,host,port,secret,protocol) VALUES(?,?,?,?,?) ON CONFLICT(instance_id) DO UPDATE SET host=excluded.host,port=excluded.port,secret=excluded.secret,protocol=excluded.protocol`, id, cfg.Host, cfg.Port, cfg.Secret, cfg.Protocol)
			if err != nil {
				return err
			}
		}
		p.committed = true
		return nil
	})
}

// Materialize uses the retained coordinator authority after SQL closure. It may
// never be called from a participant's Commit or while the SQL boundary is live.
func (s *TransactionScope) Materialize(ctx context.Context) error {
	return s.WithUse(ctx, func(operation context.Context, snapshot ScopeSnapshot) error {
		return s.state.service.materialize(operation, snapshot.Detail.Instance.ID)
	})
}

// MaterializeRestoredProperties reuses ordinary restore's manager settings and
// private credential preservation after explicit rollback pointer commit.
func (s *TransactionScope) MaterializeRestoredProperties(ctx context.Context) error {
	return s.WithUse(ctx, func(operation context.Context, snapshot ScopeSnapshot) error {
		return s.state.service.materializeRestoredProperties(operation, snapshot.Detail.Instance.ID)
	})
}
