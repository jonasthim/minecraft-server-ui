package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
	"io"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/server/providerutil"
)

// UpdateState is private coordinator history, never an API response: launch
// paths/argv remain private. It contains no manager control credentials.
type UpdateState struct {
	Revision   string             `json:"revision"`
	Generation string             `json:"generation"`
	Plan       domain.InstallPlan `json:"plan"`
	Spec       domain.LaunchSpec  `json:"spec"`
	Runtime    domain.JavaRuntime `json:"runtime"`
}

func cloneUpdateState(value UpdateState) UpdateState {
	raw, _ := json.Marshal(value)
	var out UpdateState
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *Service) UpdateSnapshot(ctx context.Context, id string) (UpdateState, error) {
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return UpdateState{}, err
	}
	current, err := s.loadInstallation(ctx, id)
	if err != nil {
		return UpdateState{}, err
	}
	if current.Plan.Selection != selection(detail) {
		return UpdateState{}, domain.E(domain.CodeConflict, "committed lifecycle selection differs from instance")
	}
	if s.runtime == nil {
		return UpdateState{}, domain.E(domain.CodeUnavailable, "runtime verification unavailable")
	}
	managed, err := s.runtime.Get(ctx, current.Spec.JavaRuntimeID)
	if err != nil {
		return UpdateState{}, safeFailure("update_runtime_identity")
	}
	if managed.ID != current.Spec.JavaRuntimeID || managed.Artifact.Checksum != current.Spec.JavaChecksum {
		return UpdateState{}, domain.E(domain.CodeConflict, "committed runtime identity changed")
	}
	return cloneUpdateState(UpdateState{detail.Instance.UpdatedAt.Format(time.RFC3339Nano), current.Generation, current.Plan, current.Spec, managed}), nil
}

// PreparedUpdate can only be constructed through an original running update job.
// It stages immutable artifacts without changing launch or selection pointers.
type PreparedUpdate struct {
	service   *Service
	execution *jobs.Execution
	owner     *PreparedUpdate
	id, jobID string
	actorID   int64
	expected  UpdateState
	next      UpdateState
}

func (p *PreparedUpdate) Snapshot() (UpdateState, error) {
	if p == nil || p.owner != p {
		return UpdateState{}, domain.E(domain.CodeForbidden, "prepared update is forged or copied")
	}
	return cloneUpdateState(p.next), nil
}
func (s *Service) updateExecution(ctx context.Context, e *jobs.Execution) (id string, actor domain.User, err error) {
	err = e.WithInstanceMutation(ctx, func(admitted string) error {
		var kind string
		var requester int64
		if err := s.db.QueryRowContext(ctx, "SELECT kind,requested_by FROM jobs WHERE instance_id=? AND state='running'", admitted).Scan(&kind, &requester); err != nil {
			return domain.E(domain.CodeForbidden, "original update requester unavailable")
		}
		if kind != "instance_update" && kind != "instance_rollback" && kind != "instance_update_prepare" {
			return domain.E(domain.CodeForbidden, "execution is not an instance update")
		}
		if err := s.actor(ctx, requester, kind == "instance_rollback"); err != nil {
			return err
		}
		actor = domain.User{ID: requester}
		if err := s.db.QueryRowContext(ctx, "SELECT username,role,disabled FROM users WHERE id=?", requester).Scan(&actor.Username, &actor.Role, &actor.Disabled); err != nil {
			return err
		}
		id = admitted
		return nil
	})
	return
}
func (s *Service) PrepareUpdateForJob(ctx context.Context, e *jobs.Execution, expected UpdateState, plan domain.InstallPlan) (*PreparedUpdate, error) {
	id, actor, err := s.updateExecution(ctx, e)
	if err != nil {
		return nil, err
	}
	current, err := s.UpdateSnapshot(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.Generation != expected.Generation || current.Revision != expected.Revision {
		return nil, domain.E(domain.CodeConflict, "installation changed before update staging")
	}
	provider := s.providers[plan.Selection.Flavor]
	if provider == nil || s.runtime == nil {
		return nil, domain.E(domain.CodeUnavailable, "managed update provider unavailable")
	}
	fresh, err := provider.Resolve(ctx, plan.Selection)
	if err != nil {
		return nil, safeFailure("update_exact_metadata")
	}
	if !providerutil.SamePlan(fresh, plan) {
		return nil, domain.E(domain.CodeConflict, "official update metadata changed since preview")
	}
	managed, err := s.runtime.EnsureRequirements(ctx, plan.Requirements, io.Discard)
	if err != nil {
		return nil, safeFailure("update_managed_java")
	}
	if managed.Major != plan.Requirements.JavaMajor || managed.Major < plan.Requirements.MinJavaMajor || plan.Requirements.MaxJavaMajor != 0 && managed.Major > plan.Requirements.MaxJavaMajor {
		return nil, domain.E(domain.CodeConflict, "update Java identity violates exact requirements")
	}
	var spec domain.LaunchSpec
	err = e.WithInstanceMutation(ctx, func(admitted string) error {
		if admitted != id {
			return domain.E(domain.CodeForbidden, "update instance changed")
		}
		if err := s.actor(ctx, actor.ID, false); err != nil {
			return err
		}
		var err error
		spec, err = provider.Install(ctx, id, plan, managed, io.Discard)
		return err
	})
	if err != nil {
		return nil, safeFailure("update_verified_distribution")
	}
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	spec, err = bindMemory(spec, detail.Instance.MemoryMiB)
	if err != nil {
		return nil, err
	}
	generation, err := key()
	if err != nil {
		return nil, err
	}
	var jobID string
	err = e.WithInstanceMutation(ctx, func(admitted string) error {
		return s.db.QueryRowContext(ctx, "SELECT id FROM jobs WHERE instance_id=? AND state='running'", admitted).Scan(&jobID)
	})
	if err != nil {
		return nil, err
	}
	p := &PreparedUpdate{service: s, execution: e, id: id, jobID: jobID, actorID: actor.ID, expected: cloneUpdateState(current), next: cloneUpdateState(UpdateState{Revision: current.Revision, Generation: generation, Plan: plan, Spec: spec, Runtime: managed})}
	p.owner = p
	return p, nil
}

type UpdateParticipant struct {
	owner         *UpdateParticipant
	active        atomic.Bool
	mu            sync.Mutex
	service       *Service
	scope         *instance.TransactionScope
	prepared      *PreparedUpdate
	sealed        bool
	transactionID string
}

// WithUpdateParticipant consumes the existing coordinator scope; it never enters
// the stopped parent again and never holds scope.WithUse across fn/WithCommit.
func (s *Service) WithUpdateParticipant(ctx context.Context, scope *instance.TransactionScope, prepared *PreparedUpdate, transactionID string, fn func(*UpdateParticipant) error) error {
	if prepared == nil || prepared.owner != prepared || prepared.service != s || fn == nil {
		return domain.E(domain.CodeForbidden, "prepared update authority unavailable")
	}
	if _, err := prepared.CheckCurrent(ctx); err != nil {
		return err
	}
	err := scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		if snapshot.Detail.Instance.ID != prepared.id || snapshot.Detail.Instance.UpdatedAt.Format(time.RFC3339Nano) != prepared.expected.Revision {
			return domain.E(domain.CodeConflict, "update stopped snapshot changed")
		}
		current, err := s.loadInstallation(operation, prepared.id)
		if err != nil {
			return err
		}
		if current.Generation != prepared.expected.Generation {
			return domain.E(domain.CodeConflict, "update generation changed")
		}
		var requester int64
		var kind string
		if err = s.db.QueryRowContext(operation, "SELECT requested_by,kind FROM jobs WHERE id=? AND instance_id=? AND state='running'", prepared.jobID, prepared.id).Scan(&requester, &kind); err != nil || requester != prepared.actorID || kind != "instance_update" {
			return domain.E(domain.CodeForbidden, "prepared update job expired")
		}
		return s.actor(operation, requester, false)
	})
	if err != nil {
		return err
	}
	p := &UpdateParticipant{service: s, scope: scope, prepared: prepared, transactionID: transactionID}
	p.owner = p
	p.active.Store(true)
	defer func() { p.active.Store(false); p.mu.Lock(); p.mu.Unlock() }()
	return fn(p)
}
func (p *UpdateParticipant) enter() (func(), error) {
	if p == nil || p.owner != p || !p.active.Load() {
		return nil, domain.E(domain.CodeForbidden, "lifecycle update participant forged or expired")
	}
	if !p.mu.TryLock() {
		return nil, domain.E(domain.CodeInstanceBusy, "lifecycle update participant overlaps")
	}
	if !p.active.Load() {
		p.mu.Unlock()
		return nil, domain.E(domain.CodeForbidden, "lifecycle update participant expired")
	}
	return p.mu.Unlock, nil
}

// Seal publishes only launch bytes. The coordinator must already have written
// its durable journal and independent boot blocker before calling Seal.
func (p *UpdateParticipant) Seal(ctx context.Context) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	return p.scope.WithUse(ctx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		marker, present, err := launcher.ReadUpdateBlocker(operation, p.service.dataDir, p.prepared.id)
		if err != nil {
			return err
		}
		if !present || marker.TransactionID != p.transactionID || marker.JobID != p.prepared.jobID || marker.Kind != "update" {
			return domain.E(domain.CodeConflict, "exact update journal blocker required before seal")
		}
		var backupID string
		if err = p.service.db.QueryRowContext(operation, "SELECT backup_id FROM update_transactions WHERE id=? AND instance_id=? AND job_id=? AND kind='update' AND state IN ('prepared','publishing')", p.transactionID, p.prepared.id, p.prepared.jobID).Scan(&backupID); err != nil {
			return err
		}
		var backupReady bool
		if err = p.service.db.QueryRowContext(operation, "SELECT EXISTS(SELECT 1 FROM backups WHERE id=? AND instance_id=? AND state='ready')", backupID, p.prepared.id).Scan(&backupReady); err != nil {
			return err
		}
		if !backupReady {
			return domain.E(domain.CodeConflict, "ready update safety archive required before seal")
		}
		if err := p.service.actor(operation, p.prepared.actorID, false); err != nil {
			return err
		}
		if err := p.service.seal(operation, p.prepared.id, p.prepared.next.Spec); err != nil {
			return err
		}
		p.sealed = true
		return nil
	})
}
func (p *UpdateParticipant) Commit(ctx context.Context, tx *sql.Tx) error {
	release, err := p.enter()
	if err != nil {
		return err
	}
	defer release()
	if !p.sealed {
		return domain.E(domain.CodeConflict, "update launch postimage was not sealed")
	}
	return p.scope.WithCommitUse(ctx, tx, func(operation context.Context, _ instance.ScopeSnapshot) error {
		var current string
		var requester int64
		var role domain.Role
		var disabled bool
		if err := tx.QueryRowContext(operation, "SELECT generation FROM lifecycle_installations WHERE instance_id=?", p.prepared.id).Scan(&current); err != nil {
			return err
		}
		if current != p.prepared.expected.Generation {
			return domain.E(domain.CodeConflict, "lifecycle generation changed before update commit")
		}
		if err := tx.QueryRowContext(operation, "SELECT requested_by FROM jobs WHERE id=? AND instance_id=? AND state='running'", p.prepared.jobID, p.prepared.id).Scan(&requester); err != nil || requester != p.prepared.actorID {
			return domain.E(domain.CodeForbidden, "update job authority changed")
		}
		if err := tx.QueryRowContext(operation, "SELECT role,disabled FROM users WHERE id=?", requester).Scan(&role, &disabled); err != nil || disabled || !role.AtLeast(domain.RoleOperator) {
			return domain.E(domain.CodeForbidden, "update role revoked before commit")
		}
		next := installation{Generation: p.prepared.next.Generation, Plan: p.prepared.next.Plan, Spec: p.prepared.next.Spec}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		// InitialLoader is false: instance selection/java and private management are
		// contributed by the instance participant in this same supplied transaction.
		return saveInstallationTx(operation, tx, p.prepared.id, next, raw)
	})
}

// SealCommittedUpdate is startup-only when called under a paused stopped scope.
// SQL already chose old/new pointers; this never reads or restores world data.
func (s *Service) SealCommittedUpdate(ctx context.Context, scope *instance.TransactionScope) error {
	return scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
		current, err := s.loadInstallation(operation, snapshot.Detail.Instance.ID)
		if err != nil {
			return err
		}
		detail, err := s.instances.Get(operation, snapshot.Detail.Instance.ID)
		if err != nil {
			return err
		}
		if current.Plan.Selection != selection(detail) {
			return domain.E(domain.CodeConflict, "committed update pointers disagree")
		}
		if s.runtime == nil {
			return domain.E(domain.CodeUnavailable, "runtime identity unavailable")
		}
		managed, err := s.runtime.Get(operation, current.Spec.JavaRuntimeID)
		if err != nil {
			return err
		}
		if managed.Artifact.Checksum != current.Spec.JavaChecksum {
			return domain.E(domain.CodeConflict, "committed update runtime changed")
		}
		return s.seal(operation, snapshot.Detail.Instance.ID, current.Spec)
	})
}

// StopForUpdate uses a stop intent already minted by scheduler.BeginUpdateStop.
// Calling the legacy hook again would invalidate that exact start guard.
func (s *Service) StopForUpdate(ctx context.Context, e *jobs.Execution, actor domain.User, countdown int) (bool, error) {
	id, fresh, err := s.updateExecution(ctx, e)
	if err != nil {
		return false, err
	}
	if fresh.ID != actor.ID {
		return false, domain.E(domain.CodeForbidden, "update stop requester differs")
	}
	var running bool
	err = e.WithInstanceMutation(ctx, func(admitted string) error {
		if admitted != id {
			return domain.E(domain.CodeForbidden, "update stop identity changed")
		}
		identity, err := s.native.Identity(ctx, id)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return safeFailure("update_process_identity")
		}
		observation, err := s.native.Inspect(ctx, identity)
		if err != nil || observation.State == domain.StateUnknown {
			return safeFailure("update_process_observation")
		}
		running = observation.ProcessRunning
		return nil
	})
	if err != nil || !running {
		return running, err
	}
	return true, s.stop(ctx, e, Request{Operation: "stop", CountdownSeconds: &countdown})
}

// StartForUpdate requires the scheduler's original one-use stop epoch guard.
// Game readiness waits occur after native start releases the stop intent mutex.
func (s *Service) StartForUpdate(ctx context.Context, e *jobs.Execution, actor domain.User, guard RecoveryStartGuard) error {
	return s.StartForUpdateWithReceipt(ctx, e, actor, guard, nil)
}
func (s *Service) StartForUpdateWithReceipt(ctx context.Context, e *jobs.Execution, actor domain.User, guard RecoveryStartGuard, onStarted func(context.Context, domain.ProcessIdentity) error) error {
	if guard == nil {
		return domain.E(domain.CodeForbidden, "update restart requires original stop epoch guard")
	}
	current, err := e.CheckCurrent(ctx)
	if err != nil {
		return err
	}
	if current.Kind != "instance_update" || current.RequesterID != actor.ID {
		return domain.E(domain.CodeForbidden, "original update restart requester required")
	}
	var started domain.ProcessIdentity
	err = e.WithInstanceMutation(ctx, func(id string) error {
		if err := s.actor(ctx, actor.ID, false); err != nil {
			return err
		}
		detail, err := s.instances.Get(ctx, id)
		if err != nil {
			return err
		}
		if !detail.EULA.Accepted {
			return domain.E(domain.CodeConflict, "explicit Minecraft EULA acceptance required")
		}
		installation, err := s.loadInstallation(ctx, id)
		if err != nil {
			return err
		}
		if installation.Plan.Selection != selection(detail) {
			return domain.E(domain.CodeConflict, "committed update pointers disagree")
		}
		var intended bool
		if err = s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM update_transactions WHERE instance_id=? AND job_id=? AND actor_id=? AND kind='update' AND state='boot_attempted' AND json_extract(journal_json,'$.boot_attempted')=1 AND json_extract(journal_json,'$.after.generation')=?)`, id, current.JobID, actor.ID, installation.Generation).Scan(&intended); err != nil {
			return err
		}
		if !intended {
			return domain.E(domain.CodeConflict, "durable exact update boot intent required")
		}
		if err = launcher.CheckActivationReady(ctx, s.dataDir, id); err != nil {
			return err
		}
		started, err = guard(ctx, id, func() (domain.ProcessIdentity, error) { return s.native.Start(ctx, id, installation.Spec) })
		return err
	})
	if err != nil {
		return err
	}
	if onStarted != nil {
		if err = onStarted(ctx, started); err != nil {
			return s.stopUnreadyUpdate(e, current.InstanceID, started, err)
		}
	}
	if err = e.Phase(ctx, "verifying_game_readiness", 90); err != nil {
		return s.stopUnreadyUpdate(e, current.InstanceID, started, err)
	}
	if s.control == nil {
		return s.stopUnreadyUpdate(e, current.InstanceID, started, domain.E(domain.CodeUnavailable, "game readiness service unavailable"))
	}
	if err = s.verifyReady(ctx, e, started); err != nil {
		return s.stopUnreadyUpdate(e, current.InstanceID, started, err)
	}
	return e.Log(ctx, "Committed update generation reached acknowledged game readiness and required settings verification.")
}

// Stop only the exact sufficiently identified process returned by this attempt.
// A replacement JVM or an uncertain receipt leaves a read-only administrative hold.
func (s *Service) stopUnreadyUpdate(e *jobs.Execution, id string, started domain.ProcessIdentity, cause error) error {
	cleanup, cancel := e.CleanupContext()
	defer cancel()
	identity, err := s.native.Identity(cleanup, id)
	if os.IsNotExist(err) {
		return domain.E(domain.CodeUnavailable, "Update did not reach readiness; JVM is stopped and world recovery is required.")
	}
	if err == nil && started.InstanceID == id && started.PID > 0 && started.StartIdentity != "" && reflect.DeepEqual(identity, started) {
		observation, inspectErr := s.native.Inspect(cleanup, identity)
		if inspectErr == nil && !observation.ProcessRunning {
			return domain.E(domain.CodeUnavailable, "Update did not reach readiness; JVM is stopped and world recovery is required.")
		}
		if inspectErr == nil && observation.State != domain.StateUnknown && s.native.Stop(cleanup, identity) == nil {
			return domain.E(domain.CodeUnavailable, "Update did not reach readiness; managed JVM stopped. Explicit world rollback is required before choosing an older generation.")
		}
	}
	return s.startFailure(e, id, "Update did not reach readiness; NEW generation and world risk remain retained.")
}

// TargetProof is diagnostic verified evidence; consumers must obtain it from the
// original PreparedUpdate again at admission and never trust caller-made values.
type TargetProof struct {
	Selection    domain.VersionSelection
	Requirements domain.LaunchRequirements
	Runtime      domain.JavaRuntime
	Language     loaderutil.LanguageEvidence
}

// WithTargetProof drains verification under the original execution or existing
// coordinator scope. It never changes a launch/selection pointer or live data.
func (p *PreparedUpdate) WithTargetProof(ctx context.Context, scope *instance.TransactionScope, fn func(context.Context, TargetProof) error) error {
	if p == nil || p.owner != p || p.service == nil || p.execution == nil || fn == nil {
		return domain.E(domain.CodeForbidden, "original staged update capability required")
	}
	verify := func(operation context.Context, id string) error {
		current, err := p.execution.CheckCurrent(operation)
		if err != nil {
			return err
		}
		if id != p.id || current.InstanceID != p.id || current.JobID != p.jobID || current.RequesterID != p.actorID {
			return domain.E(domain.CodeForbidden, "staged target belongs to a different job")
		}
		if err = p.service.actor(operation, p.actorID, false); err != nil {
			return err
		}
		provider := p.service.providers[p.next.Plan.Selection.Flavor]
		if provider == nil {
			return domain.E(domain.CodeUnavailable, "staged provider unavailable")
		}
		fresh, err := provider.Resolve(operation, p.next.Plan.Selection)
		if err != nil {
			return safeFailure("update_target_metadata")
		}
		if !providerutil.SamePlan(fresh, p.next.Plan) {
			return domain.E(domain.CodeConflict, "staged target metadata changed")
		}
		managed, err := p.service.runtime.Get(operation, p.next.Runtime.ID)
		if err != nil {
			return safeFailure("update_target_java")
		}
		if managed.ID != p.next.Runtime.ID || managed.Major != p.next.Runtime.Major || managed.Artifact.Checksum != p.next.Runtime.Artifact.Checksum {
			return domain.E(domain.CodeConflict, "staged target runtime changed")
		}
		spec, err := provider.Install(operation, p.id, p.next.Plan, managed, io.Discard)
		if err != nil {
			return safeFailure("update_target_artifacts")
		}
		detail, err := p.service.instances.Get(operation, p.id)
		if err != nil {
			return err
		}
		spec, err = bindMemory(spec, detail.Instance.MemoryMiB)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(spec, p.next.Spec) {
			return domain.E(domain.CodeConflict, "staged target launch identity changed")
		}
		language := loaderutil.LanguageEvidence{Selection: p.next.Plan.Selection, RuntimeID: managed.ID, JavaMajor: managed.Major, Languages: map[string]loaderutil.LanguageArtifact{}}
		if p.next.Plan.Selection.Flavor == domain.FlavorForge || p.next.Plan.Selection.Flavor == domain.FlavorNeoForge {
			language, err = loaderutil.VerifiedLanguageEvidence(operation, p.service.dataDir, p.id, spec)
			if err != nil {
				return domain.E(domain.CodeConflict, "target loader language evidence unavailable")
			}
		}
		return fn(operation, TargetProof{p.next.Plan.Selection, p.next.Plan.Requirements, managed, language})
	}
	if scope != nil {
		return scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
			return verify(operation, snapshot.Detail.Instance.ID)
		})
	}
	return p.execution.WithInstanceMutation(ctx, func(id string) error { return verify(ctx, id) })
}

// CheckCurrent validates the originating job without acquiring a second slot.
// It is used by inert stage readers and scoped participant lifetime checks.
func (p *PreparedUpdate) CheckCurrent(ctx context.Context) (jobs.CurrentExecution, error) {
	if p == nil || p.owner != p || p.execution == nil {
		return jobs.CurrentExecution{}, domain.E(domain.CodeForbidden, "staged target capability forged or copied")
	}
	current, err := p.execution.CheckCurrent(ctx)
	if err != nil {
		return current, err
	}
	if current.JobID != p.jobID || current.InstanceID != p.id || current.RequesterID != p.actorID {
		return jobs.CurrentExecution{}, domain.E(domain.CodeForbidden, "staged target belongs to another job")
	}
	return current, nil
}

// WithCurrentJob drains inert stage I/O under either the existing scope or the
// originating job. It verifies lifetime without repeating network resolution.
func (p *PreparedUpdate) WithCurrentJob(ctx context.Context, scope *instance.TransactionScope, fn func(context.Context) error) error {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "staged operation required")
	}
	if _, err := p.CheckCurrent(ctx); err != nil {
		return err
	}
	if scope != nil {
		return scope.WithUse(ctx, func(operation context.Context, snapshot instance.ScopeSnapshot) error {
			if snapshot.Detail.Instance.ID != p.id {
				return domain.E(domain.CodeForbidden, "staged operation scope differs")
			}
			return fn(operation)
		})
	}
	return p.execution.WithInstanceMutation(ctx, func(id string) error {
		if id != p.id {
			return domain.E(domain.CodeForbidden, "staged operation instance differs")
		}
		return fn(ctx)
	})
}

// ResolveUpdate intentionally resolves an exact target independently of initial
// install pinning. The update coordinator owns compatibility/backup/confirmation.
func (s *Service) ResolveUpdate(ctx context.Context, id string, chosen domain.VersionSelection) (domain.InstallPlan, error) {
	detail, err := s.instances.Get(ctx, id)
	if err != nil {
		return domain.InstallPlan{}, err
	}
	if detail.Provisioning != "installed" {
		return domain.InstallPlan{}, domain.E(domain.CodeInstanceNotInstalled, "install the instance before creating an update")
	}
	provider := s.providers[chosen.Flavor]
	if provider == nil {
		return domain.InstallPlan{}, domain.E(domain.CodeUnsupported, "target provider unavailable")
	}
	plan, err := provider.Resolve(ctx, chosen)
	if err != nil {
		return domain.InstallPlan{}, safeFailure("update_target_resolve")
	}
	if plan.Selection != chosen {
		return domain.InstallPlan{}, domain.E(domain.CodeConflict, "provider resolved a different update target")
	}
	return plan, nil
}
