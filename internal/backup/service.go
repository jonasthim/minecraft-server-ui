// Package backup owns save-lease capture, validated local archives and explicit
// stopped restores. External databases and manager metadata are never captured.
package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/supervisor"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

type Native interface {
	Identity(context.Context, string) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (supervisor.Observation, error)
	ArmSaveLease(context.Context, domain.ProcessIdentity, string, time.Time) error
	WithStopped(context.Context, string, func() error) error
}
type Worlds interface {
	Discover(context.Context, string) (world.Discovery, error)
}
type Config struct {
	DB        *sql.DB
	Paths     instance.Paths
	Instances *instance.Service
	Jobs      *jobs.Service
	Console   *console.Service
	Native    Native
	Worlds    Worlds
}
type Service struct {
	db        *sql.DB
	paths     instance.Paths
	instances *instance.Service
	queue     *jobs.Service
	console   *console.Service
	native    Native
	worlds    Worlds
	mu        sync.Mutex
	refs      map[string]int
	fault     func(string) error
}

func New(c Config) *Service {
	return &Service{db: c.DB, paths: c.Paths, instances: c.Instances, queue: c.Jobs, console: c.Console, native: c.Native, worlds: c.Worlds, refs: map[string]int{}}
}
func (s *Service) Register() error {
	if s.queue == nil {
		return domain.E(domain.CodeUnavailable, "backup queue unavailable")
	}
	if err := s.queue.Register("backup_create", s.runCreate); err != nil {
		return err
	}
	return s.queue.Register("backup_restore", s.runRestore)
}
func key() (string, error) {
	var b [16]byte
	_, e := rand.Read(b[:])
	return hex.EncodeToString(b[:]), e
}
func validKey(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil && strings.ToLower(id) == id
}
func stamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func authorize(actor domain.User) error {
	if actor.ID <= 0 || actor.Disabled || !actor.Role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "active operator access required")
	}
	return nil
}
func validRequest(kind domain.BackupKind, trigger domain.BackupTrigger) error {
	if kind != domain.BackupWorld && kind != domain.BackupFull {
		return domain.E(domain.CodeValidationFailed, "choose world or full backup")
	}
	if trigger != domain.BackupManual && trigger != domain.BackupAutomatic && trigger != domain.BackupSafety {
		return domain.E(domain.CodeValidationFailed, "invalid backup trigger")
	}
	return nil
}
func (s *Service) actor(ctx context.Context, id int64) (domain.User, error) {
	u := domain.User{ID: id}
	if err := s.db.QueryRowContext(ctx, `SELECT username,role,disabled FROM users WHERE id=?`, id).Scan(&u.Username, &u.Role, &u.Disabled); err != nil {
		return u, domain.E(domain.CodeForbidden, "backup requester unavailable")
	}
	return u, authorize(u)
}

// currentJob reads authority only after original execution admission supplied id.
func (s *Service) currentJob(ctx context.Context, id string) (string, domain.User, error) {
	var job string
	var actorID int64
	err := s.db.QueryRowContext(ctx, `SELECT id,requested_by FROM jobs WHERE instance_id=? AND state='running'`, id).Scan(&job, &actorID)
	if err != nil {
		return "", domain.User{}, domain.E(domain.CodeForbidden, "active backup job authority unavailable")
	}
	actor, err := s.actor(ctx, actorID)
	return job, actor, err
}
func (s *Service) audit(ctx context.Context, id string, actor domain.User, action, target, outcome string) error {
	raw, _ := json.Marshal(map[string]string{"outcome": outcome})
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json)VALUES(?,?,?,?,?,?,?)`, stamp(), actor.ID, actor.Username, "backups."+action, id, target, string(raw))
	return err
}

type createRequest struct {
	Kind    domain.BackupKind    `json:"kind"`
	Trigger domain.BackupTrigger `json:"trigger"`
}
type restoreRequest struct {
	BackupID  string `json:"backup_id"`
	Confirmed bool   `json:"confirmed"`
}

func (s *Service) Enqueue(ctx context.Context, id string, kind domain.BackupKind, trigger domain.BackupTrigger, actor domain.User) (jobs.Job, error) {
	return s.enqueue(ctx, "", id, kind, trigger, actor)
}
func (s *Service) enqueue(ctx context.Context, key, id string, kind domain.BackupKind, trigger domain.BackupTrigger, actor domain.User) (jobs.Job, error) {
	if err := authorize(actor); err != nil {
		return jobs.Job{}, err
	}
	if err := validRequest(kind, trigger); err != nil {
		return jobs.Job{}, err
	}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return jobs.Job{}, err
	}
	fresh, err := s.actor(ctx, actor.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if err = s.audit(ctx, id, fresh, "create_request", string(kind), "requested"); err != nil {
		return jobs.Job{}, err
	}
	raw, _ := json.Marshal(createRequest{kind, trigger})
	request := jobs.Request{InstanceID: id, Kind: "backup_create", Payload: raw, RequestedBy: actor.ID, Cancellable: true}
	if key != "" {
		return s.queue.EnqueueIdempotent(ctx, key, request)
	}
	return s.queue.Enqueue(ctx, request)
}
func (s *Service) EnqueueRestore(ctx context.Context, id, backupID string, confirmed bool, actor domain.User) (jobs.Job, error) {
	if err := authorize(actor); err != nil {
		return jobs.Job{}, err
	}
	if !confirmed {
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "restore requires explicit confirmation")
	}
	if _, err := s.record(ctx, id, backupID); err != nil {
		return jobs.Job{}, err
	}
	fresh, err := s.actor(ctx, actor.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if err = s.audit(ctx, id, fresh, "restore_request", backupID, "confirmed"); err != nil {
		return jobs.Job{}, err
	}
	raw, _ := json.Marshal(restoreRequest{backupID, true})
	return s.queue.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "backup_restore", Payload: raw, RequestedBy: actor.ID, Cancellable: true})
}
func (s *Service) runCreate(ctx context.Context, e *jobs.Execution) (result error) {
	defer func() {
		if result != nil {
			result = errors.Join(result, logFailure(e, result))
		}
	}()
	if err := e.Phase(ctx, "verifying_backup", 5); err != nil {
		return err
	}
	var input createRequest
	if json.Unmarshal(e.Payload, &input) != nil {
		return errCorrupt
	}
	_, err := s.CreateForJob(ctx, e, input.Kind, input.Trigger)
	return err
}
func (s *Service) failAt(phase string) error {
	if s.fault != nil {
		return s.fault(phase)
	}
	return nil
}

// CreateForJob uses the original execution and rechecks its persisted requester.
// No online backup stops a server. Unknown process state never falls back to a
// stopped copy. Stopped capture holds native admission through source staging.
func (s *Service) CreateForJob(ctx context.Context, e *jobs.Execution, kind domain.BackupKind, trigger domain.BackupTrigger) (result domain.Backup, err error) {
	if err = validRequest(kind, trigger); err != nil {
		return result, err
	}
	if e == nil || s.native == nil {
		return result, domain.E(domain.CodeUnavailable, "backup execution unavailable")
	}
	identity, probeErr := s.native.Identity(ctx, e.Job.InstanceID)
	live := false
	if probeErr == nil {
		observed, inspectErr := s.native.Inspect(ctx, identity)
		if inspectErr != nil || observed.State == domain.StateUnknown {
			return result, domain.E(domain.CodeUnavailable, "backup cannot verify JVM state")
		}
		live = observed.ProcessRunning
	} else if !os.IsNotExist(probeErr) {
		return result, domain.E(domain.CodeUnavailable, "backup cannot verify JVM identity")
	}
	if live {
		if s.console == nil {
			return result, domain.E(domain.CodeUnavailable, "online backup console unavailable")
		}
		err = s.console.WithJobSession(ctx, e, func(session *console.Session) error {
			identity, err := session.Identity(ctx)
			if err != nil {
				return err
			}
			job, actor, err := s.currentJob(ctx, identity.InstanceID)
			if err != nil {
				return err
			}
			return s.capture(ctx, identity.InstanceID, job, actor, kind, trigger, func(ctx context.Context, copy func(context.Context) error) error {
				leaseID, err := key()
				if err != nil {
					return err
				}
				raw, _ := json.Marshal(identity)
				journal := func(ctx context.Context, state string, expiry time.Time) error {
					if state == "armed" {
						_, err = s.db.ExecContext(ctx, `INSERT INTO backup_leases(id,instance_id,job_id,identity_json,expires_at,state,updated_at)VALUES(?,?,?,?,?,'armed',?)`, leaseID, identity.InstanceID, job, string(raw), expiry.UTC().Format(time.RFC3339Nano), stamp())
					} else {
						_, err = s.db.ExecContext(ctx, `UPDATE backup_leases SET state=?,updated_at=? WHERE id=?`, state, stamp(), leaseID)
					}
					return err
				}
				return captureLease(ctx, s.native, backupSession{session}, identity, leaseID, OnlineSaveLease, journal, copy)
			}, &result, e.Phase, true)
		})
	} else {
		err = s.WithStoppedJob(ctx, e, func(capability *StoppedCapture, _ *instance.StoppedSession) error {
			var err error
			result, err = capability.Capture(ctx, kind, trigger)
			return err
		})
	}
	return result, err
}

type StoppedCapture struct {
	execution *jobs.Execution
	service   *Service
	session   *instance.StoppedSession
	ctx       context.Context
	active    atomic.Bool
	owner     *StoppedCapture
	mu        sync.Mutex
	id, job   string
	actor     domain.User
}

// WithStoppedJob composes execution/native admission exactly once. Consumers
// may use Capture and the original instance session inside the callback; neither
// operation reacquires queue or native guards. Copies/expired capabilities fail.
func (s *Service) WithStoppedJob(ctx context.Context, e *jobs.Execution, fn func(*StoppedCapture, *instance.StoppedSession) error) error {
	if fn == nil {
		return errCorrupt
	}
	return s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
		detail, err := session.Detail(ctx)
		if err != nil {
			return err
		}
		job, actor, err := s.currentJob(ctx, detail.Instance.ID)
		if err != nil {
			return err
		}
		c := &StoppedCapture{execution: e, service: s, session: session, ctx: ctx, id: detail.Instance.ID, job: job, actor: actor}
		c.owner = c
		c.active.Store(true)
		defer func() { c.active.Store(false); c.mu.Lock(); c.mu.Unlock() }()
		return fn(c, session)
	})
}
func (c *StoppedCapture) Capture(ctx context.Context, kind domain.BackupKind, trigger domain.BackupTrigger) (result domain.Backup, err error) {
	if c == nil || c.owner != c || !c.active.Load() {
		return result, domain.E(domain.CodeForbidden, "stopped backup capability expired")
	}
	if !c.mu.TryLock() {
		return result, domain.E(domain.CodeInstanceBusy, "stopped backup capture active")
	}
	defer c.mu.Unlock()
	if !c.active.Load() {
		return result, domain.E(domain.CodeForbidden, "stopped backup capability expired")
	}
	if err = validRequest(kind, trigger); err != nil {
		return result, err
	}
	operation, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	if _, err = c.session.Detail(operation); err != nil {
		return result, err
	}
	err = c.service.capture(operation, c.id, c.job, c.actor, kind, trigger, func(ctx context.Context, copy func(context.Context) error) error { return copy(ctx) }, &result, c.execution.Phase, false)
	return result, err
}

const StoppedConsistency = "Captured while the managed server was verified stopped and launcher admission was held. External mod/plugin databases are excluded; arbitrary addon file formats remain unverified."
const LiveConsistency = "Save lease flushes native world saves; player data and local mod/plugin databases can still change. This is not a point-in-time snapshot. External mod/plugin databases are excluded."
const PrivateContents = "Full archives include local mod/plugin configuration and may contain their credentials. Keep downloads private. Manager database, launcher controls and manager-owned management credentials are excluded."

func (s *Service) capture(ctx context.Context, id, job string, actor domain.User, kind domain.BackupKind, trigger domain.BackupTrigger, capture func(context.Context, func(context.Context) error) error, result *domain.Backup, phase func(context.Context, string, int) error, online bool) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var outstanding bool
	if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM backup_leases WHERE instance_id=? AND state IN ('armed','unresolved'))`, id).Scan(&outstanding); err != nil {
		return err
	}
	if outstanding {
		return domain.E(domain.CodeUnavailable, "outstanding saving recovery blocks a new backup")
	}
	if err = s.CheckReady(ctx, id); err != nil {
		return err
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return err
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return err
	}
	discovery, err := s.worlds.Discover(ctx, id)
	if err != nil {
		return err
	}
	if discovery.State != "present" && !(kind == domain.BackupFull && discovery.State == "absent") {
		return domain.E(domain.CodeConflict, "saved world layout is absent, ambiguous or unsupported")
	}
	roots := []string{}
	if discovery.State == "present" {
		roots = append(roots, discovery.LevelName)
		if discovery.Layout == "paper_legacy" {
			for _, d := range discovery.Dimensions {
				if d.State == "present" {
					root := strings.Split(d.Path, "/")[0]
					found := false
					for _, v := range roots {
						found = found || v == root
					}
					if !found {
						roots = append(roots, root)
					}
				}
			}
		}
	}
	for _, root := range roots {
		if safePath(root) != nil || strings.Contains(root, "/") {
			return errCorrupt
		}
	}
	if err = phase(ctx, "preparing_backup", 10); err != nil {
		return err
	}
	backupID, err := key()
	if err != nil {
		return err
	}
	consistency := StoppedConsistency
	captureMode := "stopped_native_admission"
	if online {
		consistency = LiveConsistency
		captureMode = "acknowledged_native_save_lease"
	}
	warnings := []string{consistency, RestorePreserves}
	if kind == domain.BackupFull {
		warnings = append(warnings, PrivateContents)
	}
	*result = domain.Backup{ID: backupID, InstanceID: id, Kind: kind, Trigger: trigger, State: domain.BackupStaging, Protected: trigger != domain.BackupAutomatic, CreatedAt: time.Now().UTC(), Warnings: warnings}
	meta, _ := json.Marshal(result)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO backups(id,instance_id,job_id,state,metadata_json,created_at)VALUES(?,?,?,'staging',?,?)`, backupID, id, job, string(meta), result.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	defer func() {
		panicValue := recover()
		if panicValue != nil {
			err = errors.Join(err, errors.New("backup operation panicked after capture admission"))
		}
		defer func() {
			if panicValue != nil {
				panic(panicValue)
			}
		}()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		outcome := "validated"
		if err != nil {
			outcome = "failed_or_partial"
			result.State = domain.BackupFailed
			meta, _ := json.Marshal(result)
			_, markErr := s.db.ExecContext(cleanup, `UPDATE backups SET state='failed',metadata_json=?,recovery='Capture, saving recovery or archive publication failed; inspect job and lease history.' WHERE id=? AND state!='ready'`, string(meta), backupID)
			err = errors.Join(err, markErr)
		}
		err = errors.Join(err, s.audit(cleanup, id, actor, "create_result", backupID, outcome))
	}()
	backupRoot, err := openSource(dirs.Backups)
	if err != nil {
		return err
	}
	defer backupRoot.Close()
	defer func() {
		if err == nil {
			return
		}
		var state string
		if qerr := s.db.QueryRowContext(context.Background(), `SELECT state FROM backups WHERE id=?`, backupID).Scan(&state); qerr == nil && state != "ready" {
			if verr := backupRoot.verify(); verr == nil {
				_ = removeIfPresent(backupRoot.root, backupID+".zip")
				_ = syncDir(backupRoot.root, ".")
			}
		}
	}()
	stageName := ".stage-" + backupID
	if err = backupRoot.root.Mkdir(stageName, 0700); err != nil {
		return err
	}
	defer func() {
		if e := backupRoot.verify(); e != nil {
			err = errors.Join(err, e)
			return
		}
		err = errors.Join(err, backupRoot.root.RemoveAll(stageName))
	}()
	stage, err := openSource(filepath.Join(dirs.Backups, stageName))
	if err != nil {
		return err
	}
	defer stage.Close()
	var files []File
	if err = phase(ctx, "capturing_backup", 25); err != nil {
		return err
	}
	err = capture(ctx, func(ctx context.Context) error {
		if err := s.failAt("capture"); err != nil {
			return err
		}
		source, err := openSource(dirs.Server)
		if err != nil {
			return err
		}
		defer source.Close()
		files, err = captureTree(ctx, source, stage, roots, kind == domain.BackupFull, defaultLimits)
		return err
	})
	if err != nil {
		return err
	}
	// No compression/hash validation/publication occurs until saving cleanup has
	// completed successfully. Files were hashed as part of bounded source copying.
	if err = s.failAt("after_release"); err != nil {
		return err
	}
	files, err = s.sanitizeProperties(ctx, stage, files)
	if err != nil {
		return err
	}
	if kind == domain.BackupFull {
		roots = rootsFromFiles(files)
	}
	manifest := Manifest{CaptureMode: captureMode, WorldPresent: discovery.State == "present", LevelName: detail.Config.LevelName, Format: 1, InstanceID: id, Kind: kind, Version: detail.Instance.MinecraftVersion, Flavor: detail.Instance.Flavor, Loader: detail.Instance.LoaderVersion, Runtime: detail.Instance.JavaRuntimeID, Layout: discovery.Layout, CreatedAt: result.CreatedAt, Roots: roots, Files: files, Warnings: warnings}
	pending := backupID + ".pending"
	out, err := backupRoot.root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	defer func() { _ = backupRoot.root.Remove(pending) }()
	if err = phase(ctx, "compressing_backup", 65); err != nil {
		return err
	}
	if err = s.failAt("archive"); err != nil {
		return err
	}
	if err = writeArchive(ctx, stage, out, manifest, defaultLimits); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	info, err := out.Stat()
	if err != nil {
		return err
	}
	if err = phase(ctx, "validating_backup", 85); err != nil {
		return err
	}
	if err = s.failAt("validation"); err != nil {
		return err
	}
	if _, err = validateArchive(ctx, out, info.Size(), defaultLimits); err != nil {
		return err
	}
	if _, err = out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx, out}); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = backupRoot.verify(); err != nil {
		return err
	}
	if err = phase(ctx, "publishing_backup", 95); err != nil {
		return err
	}
	if err = s.failAt("publish"); err != nil {
		return err
	}
	pendingInfo, statErr := backupRoot.root.Lstat(pending)
	if statErr != nil {
		return statErr
	}
	if !safeInfo(pendingInfo) || !pendingInfo.Mode().IsRegular() || !os.SameFile(info, pendingInfo) || info.Size() != pendingInfo.Size() || !info.ModTime().Equal(pendingInfo.ModTime()) {
		return errStale
	}
	if err = backupRoot.root.Rename(pending, backupID+".zip"); err != nil {
		return err
	}
	published, statErr := backupRoot.root.Lstat(backupID + ".zip")
	if statErr != nil {
		return statErr
	}
	if !safeInfo(published) || !os.SameFile(info, published) || published.Size() != info.Size() || !published.ModTime().Equal(info.ModTime()) {
		return errStale
	}
	if err = syncDir(backupRoot.root, "."); err != nil {
		return err
	}
	result.State = domain.BackupReady
	result.SizeBytes = info.Size()
	result.Checksum = domain.Checksum{Algorithm: "sha256", Value: hex.EncodeToString(hash.Sum(nil))}
	now := time.Now().UTC()
	result.ValidatedAt = &now
	meta, _ = json.Marshal(result)
	raw, _ := json.Marshal(manifest)
	if err = s.failAt("commit"); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE backups SET state='ready',metadata_json=?,manifest_json=? WHERE id=? AND state='staging'`, string(meta), string(raw), backupID)
	if err != nil {
		return err
	}
	return s.applyRetention(ctx, id, DefaultRetention, time.Now().UTC())
}
func rootsFromFiles(files []File) []string {
	roots := []string{}
	seen := map[string]bool{}
	for _, f := range files {
		r := strings.Split(f.Path, "/")[0]
		if !seen[r] {
			roots = append(roots, r)
			seen[r] = true
		}
	}
	return roots
}
func (s *Service) record(ctx context.Context, id, backupID string) (domain.Backup, error) {
	var result domain.Backup
	if domain.ValidateInstanceID(id) != nil || !validKey(backupID) {
		return result, domain.E(domain.CodeValidationFailed, "invalid backup identifier")
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT metadata_json FROM backups WHERE id=? AND instance_id=? AND state='ready'`, backupID, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return result, domain.NotFound("backup")
	}
	if err != nil {
		return result, err
	}
	if json.Unmarshal([]byte(raw), &result) != nil || result.ID != backupID || result.InstanceID != id || result.State != domain.BackupReady {
		return result, errCorrupt
	}
	return result, nil
}

type Record struct {
	domain.Backup
	JobID    string `json:"job_id"`
	Recovery string `json:"recovery"`
}

func (s *Service) List(ctx context.Context, id string) ([]Record, error) {
	if _, err := s.instances.Get(ctx, id); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT metadata_json,job_id,recovery,state,COALESCE((SELECT state FROM backup_restores WHERE backup_id=backups.id AND state!='complete' ORDER BY updated_at DESC LIMIT 1),'') FROM backups WHERE instance_id=? ORDER BY created_at DESC,id DESC LIMIT 1000`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		var r Record
		var raw, state, restoreState string
		if err = rows.Scan(&raw, &r.JobID, &r.Recovery, &state, &restoreState); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(raw), &r.Backup) != nil {
			return nil, errCorrupt
		}
		if state == "deleting" {
			r.State = domain.BackupFailed
			r.Recovery = "Automatic retention deletion is pending recovery."
		}
		if restoreState != "" {
			r.Recovery = "Confirmed restore requires recovery (" + restoreState + "); server startup remains blocked. Safety backup and preimages are retained."
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type Download struct {
	*os.File
	root     *source
	release  func()
	once     sync.Once
	Filename string
	SHA256   string
}

func (d *Download) Close() error {
	var err error
	d.once.Do(func() { err = d.File.Close(); d.root.Close(); d.release() })
	return err
}
func (s *Service) Download(ctx context.Context, id, backupID string, actor domain.User) (*Download, error) {
	if err := authorize(actor); err != nil {
		return nil, err
	}
	fresh, err := s.actor(ctx, actor.ID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.refs[backupID]++
	s.mu.Unlock()
	release := func() { s.mu.Lock(); s.refs[backupID]--; s.mu.Unlock() }
	success := false
	defer func() {
		if !success {
			release()
		}
	}()
	record, err := s.record(ctx, id, backupID)
	if err != nil {
		return nil, err
	}
	dirs, err := s.paths.For(id)
	if err != nil {
		return nil, err
	}
	root, err := openSource(dirs.Backups)
	if err != nil {
		return nil, err
	}
	file, err := root.open(backupID+".zip", defaultLimits.archiveBytes)
	if err != nil {
		root.Close()
		return nil, err
	}
	if _, err = validateArchive(ctx, file.f, record.SizeBytes, defaultLimits); err != nil {
		file.f.Close()
		root.Close()
		return nil, err
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, contextReader{ctx, file.f}); err == nil && hex.EncodeToString(hash.Sum(nil)) != record.Checksum.Value {
		err = errCorrupt
	}
	if err == nil {
		_, err = file.f.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = root.verify()
	}
	if err == nil {
		err = s.audit(ctx, id, fresh, "download", backupID, "authorized_private_export")
	}
	if err != nil {
		file.f.Close()
		root.Close()
		return nil, err
	}
	success = true
	return &Download{File: file.f, root: root, release: release, Filename: id + "-" + backupID + ".zip", SHA256: record.Checksum.Value}, nil
}

func logFailure(e *jobs.Execution, err error) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	safe := domain.AsError(err)
	message := safe.Message
	if safe.Code == domain.CodeInternal {
		message = "Backup operation failed; inspect saving/restore recovery state before retrying. Local data may be partial."
	}
	return e.Log(cleanup, message)
}
