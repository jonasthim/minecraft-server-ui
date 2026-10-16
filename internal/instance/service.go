package instance

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	managedruntime "github.com/jonasthim/minecraft-server-ui/internal/runtime"
)

// MutationGuard holds jobs admission and native lifecycle exclusion throughout
// fn. Compose jobs.WithIdle -> supervisor.WithStopped -> fn; fn must not call jobs.
// Unknown process state must return unavailable; active jobs return instance_busy.
type MutationGuard interface {
	WithStopped(context.Context, string, func() error) error
}
type Draft struct {
	Instance   domain.Instance       `json:"instance"`
	Config     domain.InstanceConfig `json:"config"`
	AcceptEULA bool                  `json:"accept_eula"`
}
type EULA struct {
	Accepted   bool       `json:"accepted"`
	AcceptedBy int64      `json:"accepted_by,omitempty"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
}
type Detail struct {
	Instance     domain.Instance       `json:"instance"`
	Config       domain.InstanceConfig `json:"config"`
	EULA         EULA                  `json:"eula"`
	Provisioning string                `json:"provisioning"`
	Settings     SettingsProjection    `json:"settings"`
}
type Service struct {
	db       *sql.DB
	paths    Paths
	guard    MutationGuard
	jobGuard JobMutationGuard
	mu       sync.Mutex
}

func New(database *sql.DB, paths Paths, guard MutationGuard, options ...Option) *Service {
	s := &Service{db: database, paths: paths, guard: guard}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}
func adminOnly(u domain.User) error {
	if u.Disabled || u.ID <= 0 || u.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "administrator access required")
	}
	return nil
}
func deriveRuntime(d *Draft) error {
	requirements, err := managedruntime.Select(domain.VersionSelection{Flavor: d.Instance.Flavor, MinecraftVersion: d.Instance.MinecraftVersion, LoaderVersion: d.Instance.LoaderVersion})
	if err != nil {
		return domain.Validation([]domain.FieldError{{Field: "minecraft_version", Message: err.Error()}})
	}
	d.Instance.JavaMajor = requirements.JavaMajor
	return nil
}

func validateDraft(d Draft) error {
	if err := validID(d.Instance.ID); err != nil {
		return err
	}
	if err := d.Instance.Validate(); err != nil {
		return domain.Validation([]domain.FieldError{{Field: "instance", Message: err.Error()}})
	}
	if d.Instance.JavaRuntimeID != "" {
		return domain.E(domain.CodeValidationFailed, "runtime selection is managed by provisioning")
	}
	if len(d.Instance.LoaderVersion) > 128 {
		return domain.E(domain.CodeValidationFailed, "loader version exceeds 128 bytes")
	}
	if d.Instance.Flavor == domain.FlavorVanilla && d.Instance.LoaderVersion != "" {
		return domain.E(domain.CodeValidationFailed, "loader version is not applicable to this flavor")
	}
	if d.Instance.Flavor == domain.FlavorPaper && d.Instance.LoaderVersion != "" {
		build, err := strconv.ParseUint(d.Instance.LoaderVersion, 10, 64)
		if err != nil || build == 0 || strings.Trim(d.Instance.LoaderVersion, "0123456789") != "" {
			return domain.Validation([]domain.FieldError{{Field: "loader_version", Message: "Paper build must be a positive numeric published build"}})
		}
	}
	return ValidateConfig(d.Config)
}

const selectDetail = `SELECT i.id,i.name,i.flavor,i.minecraft_version,i.loader_version,i.java_major,i.java_runtime_id,i.memory_mib,i.game_port,i.config_json,i.created_at,i.updated_at,COALESCE(s.eula_accepted,0),s.eula_accepted_by,s.eula_accepted_at,COALESCE(s.provisioning,'not_installed') FROM instances i LEFT JOIN instance_settings s ON s.instance_id=i.id`

type scanner interface{ Scan(...any) error }

func scanDetail(row scanner) (Detail, error) {
	var d Detail
	var config, created, updated string
	var by sql.NullInt64
	var at sql.NullString
	err := row.Scan(&d.Instance.ID, &d.Instance.Name, &d.Instance.Flavor, &d.Instance.MinecraftVersion, &d.Instance.LoaderVersion, &d.Instance.JavaMajor, &d.Instance.JavaRuntimeID, &d.Instance.MemoryMiB, &d.Instance.GamePort, &config, &created, &updated, &d.EULA.Accepted, &by, &at, &d.Provisioning)
	if errors.Is(err, sql.ErrNoRows) {
		return d, domain.NotFound("instance")
	}
	if err != nil {
		return d, fmt.Errorf("read instance: %w", err)
	}
	if err = json.Unmarshal([]byte(config), &d.Config); err != nil {
		return d, fmt.Errorf("decode instance configuration: %w", err)
	}
	if d.Config.Extra == nil {
		d.Config.Extra = map[string]string{}
	}
	d.Instance.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return d, fmt.Errorf("decode instance creation time: %w", err)
	}
	d.Instance.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return d, fmt.Errorf("decode instance update time: %w", err)
	}
	if by.Valid {
		d.EULA.AcceptedBy = by.Int64
	}
	if at.Valid {
		t, e := time.Parse(time.RFC3339Nano, at.String)
		if e != nil {
			return d, e
		}
		d.EULA.AcceptedAt = &t
	}
	return detailWithSettings(d), nil
}
func (s *Service) List(ctx context.Context) ([]Detail, error) {
	rows, err := s.db.QueryContext(ctx, selectDetail+` ORDER BY i.created_at,i.id`)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	defer rows.Close()
	out := []Detail{}
	for rows.Next() {
		d, err := scanDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Service) Get(ctx context.Context, id string) (Detail, error) {
	if err := validID(id); err != nil {
		return Detail{}, err
	}
	return scanDetail(s.db.QueryRowContext(ctx, selectDetail+` WHERE i.id=?`, id))
}
func audit(ctx context.Context, tx *sql.Tx, actor domain.User, id, action string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,?,'{}')`, time.Now().UTC().Format(time.RFC3339Nano), actor.ID, actor.Username, action, id, id)
	if err != nil {
		return fmt.Errorf("audit instance mutation: %w", err)
	}
	return nil
}
func portFree(ctx context.Context, tx *sql.Tx, id string, port int) error {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM instance_tcp_ports WHERE port=? AND NOT(instance_id=? AND kind='game')`, port, id).Scan(&exists)
	if err == nil {
		return domain.E(domain.CodePortInUse, "TCP port is reserved by another instance protocol")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check game port: %w", err)
	}
	return nil
}
func (s *Service) Create(ctx context.Context, actor domain.User, d Draft) (Detail, error) {
	if err := adminOnly(actor); err != nil {
		return Detail{}, err
	}
	if err := deriveRuntime(&d); err != nil {
		return Detail{}, err
	}
	if err := validateDraft(d); err != nil {
		return Detail{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	dirs, err := s.paths.For(d.Instance.ID)
	if err != nil {
		return Detail{}, err
	}
	if err = os.MkdirAll(filepath.Dir(dirs.Root), 0700); err != nil {
		return Detail{}, fmt.Errorf("create instance parent: %w", err)
	}
	parent, err := os.OpenRoot(filepath.Dir(dirs.Root))
	if err != nil {
		return Detail{}, err
	}
	defer parent.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, err
	}
	defer tx.Rollback()
	if err = portFree(ctx, tx, d.Instance.ID, d.Instance.GamePort); err != nil {
		return Detail{}, err
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM instance_mutations WHERE instance_id=?`, d.Instance.ID).Scan(&pending); err != nil {
		return Detail{}, err
	}
	if pending > 0 {
		return Detail{}, domain.E(domain.CodeConflict, "instance deletion requires reconciliation before reusing this id")
	}
	var present int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM instances WHERE id=?`, d.Instance.ID).Scan(&present)
	if err == nil {
		return Detail{}, domain.E(domain.CodeConflict, "instance id already exists")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Detail{}, err
	}
	if err = parent.Mkdir(d.Instance.ID, 0700); err != nil {
		if os.IsExist(err) {
			return Detail{}, domain.E(domain.CodeConflict, "instance directory already exists")
		}
		return Detail{}, fmt.Errorf("create private instance directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = parent.RemoveAll(d.Instance.ID)
		}
	}()
	root, err := parent.OpenRoot(d.Instance.ID)
	if err != nil {
		return Detail{}, err
	}
	defer root.Close()
	for _, area := range []string{"server", "logs", "backups", "state"} {
		if err = root.Mkdir(area, 0700); err != nil {
			return Detail{}, err
		}
	}
	ownershipToken, err := randomID()
	if err != nil {
		return Detail{}, err
	}
	if err = atomicFile(ctx, root, "state/instance-owner", []byte(ownershipToken)); err != nil {
		return Detail{}, err
	}
	now := time.Now().UTC()
	d.Instance.CreatedAt = now
	d.Instance.UpdatedAt = now
	eula := EULA{}
	if d.AcceptEULA {
		eula = EULA{true, actor.ID, &now}
	}
	if err = writeManaged(ctx, root, d.Instance, d.Config, eula); err != nil {
		return Detail{}, err
	}
	config, err := json.Marshal(d.Config)
	if err != nil {
		return Detail{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO instances(id,name,flavor,minecraft_version,loader_version,java_major,java_runtime_id,memory_mib,game_port,config_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, d.Instance.ID, d.Instance.Name, d.Instance.Flavor, d.Instance.MinecraftVersion, d.Instance.LoaderVersion, d.Instance.JavaMajor, d.Instance.JavaRuntimeID, d.Instance.MemoryMiB, d.Instance.GamePort, string(config), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return Detail{}, fmt.Errorf("create instance: %w", err)
	}
	var by, at any
	if eula.Accepted {
		by = actor.ID
		at = now.Format(time.RFC3339Nano)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO instance_settings(instance_id,eula_accepted,eula_accepted_by,eula_accepted_at,ownership_token) VALUES(?,?,?,?,?)`, d.Instance.ID, eula.Accepted, by, at, ownershipToken)
	if err != nil {
		return Detail{}, err
	}
	if err = audit(ctx, tx, actor, d.Instance.ID, "instance.create"); err != nil {
		return Detail{}, err
	}
	if d.AcceptEULA {
		if err = audit(ctx, tx, actor, d.Instance.ID, "instance.eula.accept"); err != nil {
			return Detail{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Detail{}, fmt.Errorf("commit instance creation: %w", err)
	}
	committed = true
	return detailWithSettings(Detail{Instance: d.Instance, Config: d.Config, EULA: eula, Provisioning: "not_installed"}), nil
}
func (s *Service) mutate(ctx context.Context, id string, fn func() error) error {
	if err := validID(id); err != nil {
		return err
	}
	if s.guard == nil {
		return domain.E(domain.CodeUnavailable, "native lifecycle verification is unavailable; instance mutation is blocked")
	}
	return s.guard.WithStopped(ctx, id, func() error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	})
}
func (s *Service) Update(ctx context.Context, actor domain.User, id string, d Draft) (Detail, error) {
	if err := adminOnly(actor); err != nil {
		return Detail{}, err
	}
	if d.Instance.ID != id {
		return Detail{}, domain.E(domain.CodeValidationFailed, "instance id cannot be changed")
	}
	if err := deriveRuntime(&d); err != nil {
		return Detail{}, err
	}
	if err := validateDraft(d); err != nil {
		return Detail{}, err
	}
	var result Detail
	err := s.mutate(ctx, id, func() error {
		if err := s.recoverDeletion(ctx, id); err != nil {
			return err
		}
		previous, err := s.Get(ctx, id)
		if err != nil {
			return err
		}
		if d.Config.Extra == nil {
			d.Config.Extra = previous.Config.Extra
		}
		if d.Config.LevelName != previous.Config.LevelName {
			return domain.E(domain.CodeConflict, "world directory cannot be changed; each instance has one world")
		}
		if d.Instance.Flavor != previous.Instance.Flavor || d.Instance.MinecraftVersion != previous.Instance.MinecraftVersion || d.Instance.LoaderVersion != previous.Instance.LoaderVersion || d.Instance.JavaMajor != previous.Instance.JavaMajor {
			return domain.E(domain.CodeUnsupported, "version changes require the provider compatibility and safety-backup workflow")
		}
		dirs, err := s.paths.For(id)
		if err != nil {
			return err
		}
		root, err := os.OpenRoot(dirs.Root)
		if err != nil {
			return fmt.Errorf("open instance root: %w", err)
		}
		defer root.Close()
		if err = s.verifyOwner(ctx, id, root); err != nil {
			return err
		}
		if err = s.recoverConfig(ctx, root, previous); err != nil {
			return err
		}
		token, err := s.ownerToken(ctx, id)
		if err != nil {
			return err
		}
		originalFiles, err := snapshotManaged(root)
		if err != nil {
			return err
		}
		existingExtras, err := unknownProperties(root)
		if err != nil {
			return domain.Wrap(domain.CodeValidationFailed, "existing server.properties cannot be safely read", err)
		}
		mergeFileOnlyExtras(&d.Config, existingExtras, previous.Config.Extra)
		if err = ValidateConfig(d.Config); err != nil {
			return err
		}
		management, err := s.optionalManagement(ctx, id)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = portFree(ctx, tx, id, d.Instance.GamePort); err != nil {
			return err
		}
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE instance_id=? AND state IN ('queued','running')`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return domain.E(domain.CodeInstanceBusy, "instance has queued or running jobs")
		}
		d.Instance.CreatedAt = previous.Instance.CreatedAt
		d.Instance.UpdatedAt = time.Now().UTC()
		if !d.Instance.UpdatedAt.After(previous.Instance.UpdatedAt) {
			d.Instance.UpdatedAt = previous.Instance.UpdatedAt.Add(time.Nanosecond)
		}
		eula := previous.EULA
		newAcceptance := d.AcceptEULA && !eula.Accepted
		if newAcceptance {
			now := d.Instance.UpdatedAt
			eula = EULA{true, actor.ID, &now}
		}
		config, err := json.Marshal(d.Config)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE instances SET name=?,memory_mib=?,game_port=?,config_json=?,updated_at=? WHERE id=?`, d.Instance.Name, d.Instance.MemoryMiB, d.Instance.GamePort, string(config), d.Instance.UpdatedAt.Format(time.RFC3339Nano), id)
		if err != nil {
			return err
		}
		if newAcceptance {
			_, err = tx.ExecContext(ctx, `UPDATE instance_settings SET eula_accepted=1,eula_accepted_by=?,eula_accepted_at=? WHERE instance_id=?`, actor.ID, eula.AcceptedAt.Format(time.RFC3339Nano), id)
			if err != nil {
				return err
			}
			if err = audit(ctx, tx, actor, id, "instance.eula.accept"); err != nil {
				return err
			}
		}
		if err = audit(ctx, tx, actor, id, "instance.update"); err != nil {
			return err
		}
		if err = persistConfigIntent(ctx, root, configIntent{OwnerToken: token, BeforeUpdatedAt: previous.Instance.UpdatedAt, AfterUpdatedAt: d.Instance.UpdatedAt, Before: originalFiles}); err != nil {
			return err
		}
		if err = writeManaged(ctx, root, d.Instance, d.Config, eula, management); err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restoreErr := rollbackConfig(cleanup, root, originalFiles)
			return errors.Join(err, restoreErr)
		}
		if err = tx.Commit(); err != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			restoreErr := rollbackConfig(cleanup, root, originalFiles)
			return errors.Join(fmt.Errorf("commit configuration: %w", err), restoreErr)
		}
		result = detailWithSettings(Detail{Instance: d.Instance, Config: d.Config, EULA: eula, Provisioning: previous.Provisioning})
		return clearConfigIntent(root)
	})
	return result, err
}
func (s *Service) Delete(ctx context.Context, actor domain.User, id, confirmation string) error {
	if err := adminOnly(actor); err != nil {
		return err
	}
	if confirmation != id {
		return domain.E(domain.CodeConflict, "type the instance id to confirm permanent deletion")
	}
	return s.mutate(ctx, id, func() error {
		if err := s.recoverDeletion(ctx, id); err != nil {
			return err
		}
		if _, err := s.Get(ctx, id); err != nil {
			return err
		}
		dirs, err := s.paths.For(id)
		if err != nil {
			return err
		}
		parent, err := os.OpenRoot(filepath.Dir(dirs.Root))
		if err != nil {
			return err
		}
		defer parent.Close()
		root, err := parent.OpenRoot(id)
		if err != nil {
			return err
		}
		token, err := s.ownerToken(ctx, id)
		if err == nil {
			err = verifyToken(root, token)
		}
		root.Close()
		if err != nil {
			return err
		}
		suffix, err := randomID()
		if err != nil {
			return err
		}
		trash := ".deleting-" + id + "-" + suffix
		// The durable intent precedes the directory rename and survives row cascade.
		// Replay uses its secret ownership token and never adopts an unknown path.
		_, err = s.db.ExecContext(ctx, `INSERT INTO instance_mutations(instance_id,kind,staged_name,ownership_token) VALUES(?,'delete',?,?)`, id, trash, token)
		if err != nil {
			return fmt.Errorf("journal instance deletion: %w", err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var active int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE instance_id=? AND state IN ('queued','running')`, id).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return domain.E(domain.CodeInstanceBusy, "instance has queued or running jobs")
		}
		if err = audit(ctx, tx, actor, id, "instance.delete"); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM instances WHERE id=?`, id); err != nil {
			return err
		}
		if err = parent.Rename(id, trash); err != nil {
			return fmt.Errorf("stage instance deletion: %w", err)
		}
		if err = syncDirectory(parent, "."); err != nil {
			return errors.Join(err, parent.Rename(trash, id))
		}
		if err = tx.Commit(); err != nil {
			return errors.Join(fmt.Errorf("commit deletion: %w", err), parent.Rename(trash, id))
		}
		if err = cleanupOwnedDeletion(ctx, parent, trash, token); err != nil {
			return domain.Wrap(domain.CodeInternal, "instance record deleted; private directory cleanup remains journaled for reconciliation", err)
		}
		if _, err = s.db.ExecContext(ctx, `DELETE FROM instance_mutations WHERE instance_id=?`, id); err != nil {
			return fmt.Errorf("finish deletion journal: %w", err)
		}
		return nil
	})
}

func randomID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func writeManaged(ctx context.Context, root *os.Root, i domain.Instance, c domain.InstanceConfig, e EULA, management ...*ManagementConfig) error {
	var private *ManagementConfig
	if len(management) > 0 {
		private = management[0]
	}
	if err := atomicFile(ctx, root, "server/server.properties", propertiesWithManagement(i, c, private)); err != nil {
		return err
	}
	value := "eula=false\n"
	if e.Accepted {
		value = "eula=true\n"
	}
	return atomicFile(ctx, root, "server/eula.txt", []byte(value))
}
func atomicFile(ctx context.Context, root *os.Root, path string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	tmp := path + ".tmp-" + id
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("stage instance file: %w", err)
	}
	defer root.Remove(tmp)
	if _, err = io.Copy(f, bytes.NewReader(data)); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return fmt.Errorf("write instance file: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = root.Rename(tmp, path); err != nil {
		return fmt.Errorf("activate instance file: %w", err)
	}
	return syncDirectory(root, filepath.Dir(path))
}

func detailWithSettings(d Detail) Detail { d.Settings = SettingsFor(d.Instance, d.Config); return d }
