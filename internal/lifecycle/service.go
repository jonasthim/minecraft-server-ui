// Package lifecycle coordinates durable jobs over managed providers, verified
// activation, native supervision and acknowledged game readiness.
package lifecycle

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/safepath"
	"github.com/jonasthim/minecraft-server-ui/internal/server/launchargs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
)

type Native interface {
	Start(context.Context, string, domain.LaunchSpec) (domain.ProcessIdentity, error)
	Stop(context.Context, domain.ProcessIdentity) error
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
	WithStopped(context.Context, string, func() error) error
	SetAutostart(context.Context, string, bool) error
}
type Runtime interface {
	EnsureRequirements(context.Context, domain.LaunchRequirements, io.Writer) (domain.JavaRuntime, error)
	Get(context.Context, string) (domain.JavaRuntime, error)
}
type Config struct {
	DB        *sql.DB
	DataDir   string
	Instances *instance.Service
	Jobs      *jobs.Service
	Native    Native
	Runtime   Runtime
	Providers []domain.DistributionProvider
	Console   *console.Service
}
type Service struct {
	db        *sql.DB
	dataDir   string
	instances *instance.Service
	queue     *jobs.Service
	native    Native
	runtime   Runtime
	providers map[domain.Flavor]domain.DistributionProvider
	control   *console.Service
	seal      func(context.Context, string, domain.LaunchSpec) error
}

func New(c Config) *Service {
	s := &Service{db: c.DB, dataDir: c.DataDir, instances: c.Instances, queue: c.Jobs, native: c.Native, runtime: c.Runtime, providers: map[domain.Flavor]domain.DistributionProvider{}, control: c.Console}
	for _, p := range c.Providers {
		s.providers[p.Flavor()] = p
	}
	s.seal = func(ctx context.Context, id string, spec domain.LaunchSpec) error {
		if err := launcher.Seal(ctx, s.dataDir, id, spec); err != nil {
			return err
		}
		paths, err := launcher.Paths(s.dataDir, id)
		if err != nil {
			return err
		}
		root, err := safepath.OpenRoot(ctx, paths.State)
		if err != nil {
			return err
		}
		defer root.Close()
		return syncDirectory(root)
	}
	return s
}
func selection(d instance.Detail) domain.VersionSelection {
	return domain.VersionSelection{Flavor: d.Instance.Flavor, MinecraftVersion: d.Instance.MinecraftVersion, LoaderVersion: d.Instance.LoaderVersion}
}
func activeUser(u domain.User, admin bool) error {
	if u.Disabled || u.ID <= 0 || (u.Role != domain.RoleAdmin && (admin || u.Role != domain.RoleOperator)) {
		return domain.E(domain.CodeForbidden, "active lifecycle role required")
	}
	return nil
}
func key() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

type Preview struct {
	ID           string                    `json:"id"`
	Selection    domain.VersionSelection   `json:"selection"`
	Requirements domain.LaunchRequirements `json:"requirements"`
	Artifacts    []PreviewArtifact         `json:"artifacts"`
	Warnings     []string                  `json:"warnings"`
	ExpiresAt    time.Time                 `json:"expires_at"`
}
type PreviewArtifact struct {
	Filename string          `json:"filename"`
	Size     int64           `json:"size"`
	Checksum domain.Checksum `json:"checksum"`
}
type Catalog struct {
	Versions      []string `json:"versions"`
	Loaders       []string `json:"loaders"`
	Qualification string   `json:"qualification"`
}

func (s *Service) Catalog(ctx context.Context, flavor domain.Flavor, mc string) (Catalog, error) {
	out := Catalog{Versions: []string{}, Loaders: []string{}, Qualification: "Discovery candidates; installation and platform/game qualification are separate."}
	p := s.providers[flavor]
	if p == nil {
		return out, domain.E(domain.CodeUnsupported, "distribution is unavailable")
	}
	v, ok := p.(interface {
		Versions(context.Context) ([]string, error)
	})
	if !ok {
		return out, domain.E(domain.CodeUnavailable, "distribution catalog is unavailable")
	}
	var err error
	out.Versions, err = v.Versions(ctx)
	if err != nil {
		return out, domain.E(domain.CodeUpstreamError, "official distribution catalog could not be loaded")
	}
	if mc != "" {
		switch l := p.(type) {
		case interface {
			Builds(context.Context, string) ([]string, error)
		}:
			out.Loaders, err = l.Builds(ctx, mc)
		case interface {
			Loaders(context.Context, string) ([]string, error)
		}:
			out.Loaders, err = l.Loaders(ctx, mc)
		}
		if err != nil {
			return out, domain.E(domain.CodeUpstreamError, "official loader catalog could not be loaded")
		}
	}
	return out, nil
}
func (s *Service) Preview(ctx context.Context, id string, chosen domain.VersionSelection) (Preview, error) {
	d, err := s.instances.Get(ctx, id)
	if err != nil {
		return Preview{}, err
	}
	if err = s.allowSelection(ctx, d, chosen); err != nil {
		return Preview{}, err
	}
	p := s.providers[chosen.Flavor]
	if p == nil {
		return Preview{}, domain.E(domain.CodeUnsupported, "distribution is unavailable")
	}
	plan, err := p.Resolve(ctx, chosen)
	if err != nil {
		return Preview{}, domain.E(domain.CodeUpstreamError, "exact official distribution selection could not be resolved")
	}
	if plan.Selection != chosen {
		return Preview{}, domain.E(domain.CodeUpstreamError, "provider resolved a different selection")
	}
	if plan.Requirements.JavaMajor != d.Instance.JavaMajor {
		return Preview{}, domain.E(domain.CodeUnsupported, "resolved Java requirement differs from the persisted compatibility selection")
	}
	idPreview, err := key()
	if err != nil {
		return Preview{}, err
	}
	expiry := time.Now().UTC().Add(30 * time.Minute)
	raw, err := json.Marshal(plan)
	if err != nil {
		return Preview{}, err
	}
	original, err := json.Marshal(selection(d))
	if err != nil {
		return Preview{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lifecycle_previews(id,instance_id,revision,original_selection_json,expires_at,plan_json) VALUES(?,?,?,?,?,?)`, idPreview, id, d.Instance.UpdatedAt.Format(time.RFC3339Nano), string(original), expiry.Format(time.RFC3339Nano), string(raw))
	if err != nil {
		return Preview{}, fmt.Errorf("persist install preview: %w", err)
	}
	out := Preview{ID: idPreview, Selection: chosen, Requirements: plan.Requirements, Artifacts: []PreviewArtifact{}, Warnings: append([]string{"Catalog and installer evidence do not establish tested game support."}, plan.Warnings...), ExpiresAt: expiry}
	for _, a := range plan.Artifacts {
		out.Artifacts = append(out.Artifacts, PreviewArtifact{a.Filename, a.Size, a.Checksum})
	}
	return out, nil
}
func bindMemory(spec domain.LaunchSpec, mib int) (domain.LaunchSpec, error) {
	if mib < 256 || mib > 1048576 {
		return spec, domain.E(domain.CodeValidationFailed, "instance memory is outside approved bounds")
	}
	base, err := launchargs.StripMemory(spec.Args)
	if err != nil {
		return spec, err
	}
	prefix := []string{"-Xms" + strconv.Itoa(min(512, mib)) + "M", "-Xmx" + strconv.Itoa(mib) + "M"}
	if len(base) != len(spec.Args) && (spec.Args[0] != prefix[0] || spec.Args[1] != prefix[1]) {
		return spec, domain.E(domain.CodeConflict, "provider memory differs from instance configuration")
	}
	spec.Args = append(prefix, base...)
	if len(spec.Env) > 0 {
		return spec, domain.E(domain.CodeUnsupported, "launch environment overrides are unsupported")
	}
	return spec, nil
}

type installation struct {
	OriginalRevision string             `json:"original_revision,omitempty"`
	InitialLoader    bool               `json:"initial_loader,omitempty"`
	Generation       string             `json:"generation"`
	Plan             domain.InstallPlan `json:"plan"`
	Spec             domain.LaunchSpec  `json:"spec"`
}

func (s *Service) loadInstallation(ctx context.Context, id string) (installation, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT installation_json FROM lifecycle_installations WHERE instance_id=?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return installation{}, domain.E(domain.CodeInstanceNotInstalled, "instance has no activated installation")
	}
	if err != nil {
		return installation{}, err
	}
	var in installation
	err = json.Unmarshal([]byte(raw), &in)
	return in, err
}
func (s *Service) intent(ctx context.Context, id string, in installation) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO lifecycle_activations(instance_id,generation,installation_json) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET generation=excluded.generation,installation_json=excluded.installation_json`, id, in.Generation, string(raw))
	return err
}
func (s *Service) saveInstallation(ctx context.Context, id string, in installation) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveInstallationTx(ctx, tx, id, in, raw); err != nil {
		return err
	}

	return tx.Commit()
}
func saveInstallationTx(ctx context.Context, tx *sql.Tx, id string, in installation, raw []byte) error {
	var err error
	if _, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_generations(instance_id,generation,installation_json) VALUES(?,?,?) ON CONFLICT DO NOTHING`, id, in.Generation, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO lifecycle_installations(instance_id,generation,installation_json) VALUES(?,?,?) ON CONFLICT(instance_id) DO UPDATE SET generation=excluded.generation,installation_json=excluded.installation_json`, id, in.Generation, string(raw)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE instance_settings SET provisioning='installed' WHERE instance_id=?`, id); err != nil {
		return err
	}
	if in.InitialLoader {
		res, e := tx.ExecContext(ctx, `UPDATE instances SET loader_version=?,java_runtime_id=?,updated_at=? WHERE id=? AND loader_version='' AND updated_at=? AND java_major=?`, in.Plan.Selection.LoaderVersion, in.Spec.JavaRuntimeID, stamp(), id, in.OriginalRevision, in.Plan.Requirements.JavaMajor)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return domain.E(domain.CodeConflict, "initial loader selection changed before activation commit")
		}
	} else if _, err = tx.ExecContext(ctx, `UPDATE instances SET java_runtime_id=? WHERE id=?`, in.Spec.JavaRuntimeID, id); err != nil {
		return err
	}
	return nil
}

func (s *Service) clearIntent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM lifecycle_activations WHERE instance_id=?`, id)
	return err
}
func (s *Service) recoverActivation(ctx context.Context, id string) error {
	var pending int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM lifecycle_activations WHERE instance_id=?`, id).Scan(&pending); err != nil || pending == 0 {
		return err
	}
	pendingConfig, err := launcher.PendingConfig(s.dataDir, id)
	if err != nil {
		return err
	}
	if pendingConfig {
		return domain.E(domain.CodeUnavailable, "configuration activation pending; instance configuration recovery must finish first")
	}
	current, err := s.loadInstallation(ctx, id)
	if err == nil {
		if err = s.seal(ctx, id, current.Spec); err != nil {
			return fmt.Errorf("restore committed activation: %w", err)
		}
	} else if domain.AsError(err).Code == domain.CodeInstanceNotInstalled {
		if err = s.removeActivation(ctx, id); err != nil {
			return err
		}
	} else {
		return err
	}
	if err = launcher.ClearActivationPending(ctx, s.dataDir, id); err != nil {
		return err
	}
	return s.clearIntent(ctx, id)
}
func (s *Service) removeActivation(ctx context.Context, id string) error {
	p, err := launcher.Paths(s.dataDir, id)
	if err != nil {
		return err
	}
	root, err := safepath.OpenRoot(ctx, p.State)
	if err != nil {
		return err
	}
	defer root.Close()
	err = root.Remove("launch.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(root)
}
func (s *Service) Reconcile(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id FROM lifecycle_activations ORDER BY instance_id`)
	if err != nil {
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
		if err = s.native.WithStopped(ctx, id, func() error { return s.recoverActivation(ctx, id) }); err != nil {
			return fmt.Errorf("reconcile lifecycle activation %s: %w", id, err)
		}
	}
	return nil
}

// State exposes process observation separately from game readiness and private
// launch data. An applied autostart flag is only the last successful native call.
type State struct {
	Installed        bool                     `json:"installed"`
	Selection        *domain.VersionSelection `json:"selection,omitempty"`
	Process          string                   `json:"process"`
	ProcessRunning   bool                     `json:"process_running"`
	Readiness        string                   `json:"readiness"`
	AutostartDesired bool                     `json:"autostart_desired"`
	AutostartApplied *bool                    `json:"autostart_applied"`
	AutostartError   string                   `json:"autostart_error,omitempty"`
}

func (s *Service) State(ctx context.Context, id string) (State, error) {
	out := State{Process: "unknown", Readiness: "unverified"}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return out, err
	}
	in, err := s.loadInstallation(ctx, id)
	if err == nil {
		out.Installed = true
		chosen := in.Plan.Selection
		out.Selection = &chosen
	} else if domain.AsError(err).Code != domain.CodeInstanceNotInstalled {
		return out, err
	}
	var applied sql.NullBool
	err = s.db.QueryRowContext(ctx, `SELECT desired,applied,error FROM lifecycle_autostart WHERE instance_id=?`, id).Scan(&out.AutostartDesired, &applied, &out.AutostartError)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if applied.Valid {
		out.AutostartApplied = &applied.Bool
	}
	if s.native != nil {
		identity, err := s.native.Identity(ctx, id)
		if os.IsNotExist(err) {
			out.Process = "stopped"
		} else if err == nil {
			observed, e := s.native.Inspect(ctx, identity)
			out.ProcessRunning = observed.ProcessRunning
			if e == nil {
				out.Process = string(observed.State)
			}
		}
	}
	if s.control != nil {
		state, e := s.control.Status(ctx, id)
		if e == nil {
			out.Readiness = state.Status
		}
	}
	return out, nil
}

func (s *Service) audit(ctx context.Context, u domain.User, id, action string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,?,?,?,?,?,'{}')`, stamp(), u.ID, u.Username, "lifecycle."+action+".request", id, id)
	return err
}
func (s *Service) actor(ctx context.Context, id int64, admin bool) error {
	var u domain.User
	u.ID = id
	if err := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, id).Scan(&u.Username, &u.Role, &u.Disabled); err != nil {
		return domain.E(domain.CodeForbidden, "lifecycle requester is unavailable")
	}
	return activeUser(u, admin)
}
func safeFailure(phase string) error {
	return domain.E(domain.CodeUnavailable, "Lifecycle "+strings.ReplaceAll(phase, "_", " ")+" failed; inspect job phase and current process state before retrying.")
}

func (s *Service) allowSelection(ctx context.Context, d instance.Detail, chosen domain.VersionSelection) error {
	original := selection(d)
	if original == chosen {
		return nil
	}
	if original.Flavor != chosen.Flavor || original.MinecraftVersion != chosen.MinecraftVersion || original.LoaderVersion != "" || chosen.LoaderVersion == "" || d.Provisioning != "not_installed" {
		return domain.E(domain.CodeUnsupported, "selection is pinned; version changes require compatibility and safety backup")
	}
	var history int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM lifecycle_installations WHERE instance_id=?)+(SELECT COUNT(*) FROM lifecycle_generations WHERE instance_id=?)`, d.Instance.ID, d.Instance.ID).Scan(&history); err != nil {
		return err
	}
	if history != 0 {
		return domain.E(domain.CodeConflict, "installation history prevents initial loader selection")
	}
	return nil
}
