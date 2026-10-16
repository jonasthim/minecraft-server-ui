package lifecycle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/launcher"
	"github.com/jonasthim/minecraft-server-ui/internal/server/launchargs"
)

type Request struct {
	Operation        string `json:"operation"`
	PreviewID        string `json:"preview_id,omitempty"`
	CountdownSeconds *int   `json:"countdown_seconds,omitempty"`
	Emergency        bool   `json:"emergency,omitempty"`
	Confirmation     string `json:"confirmation,omitempty"`
	Enabled          bool   `json:"enabled,omitempty"`
}
type jobInput struct {
	Request
	ActorID int64 `json:"actor_id"`
}

func (s *Service) RegisterJobs() error {
	if s.queue == nil {
		return domain.E(domain.CodeUnavailable, "lifecycle queue is unavailable")
	}
	if err := s.queue.Register("lifecycle", s.run); err != nil {
		return err
	}
	return s.queue.Register("lifecycle_verify", s.runVerify)
}
func (s *Service) Enqueue(ctx context.Context, u domain.User, id string, in Request) (jobs.Job, error) {
	return s.enqueue(ctx, "", u, id, in)
}
func (s *Service) enqueue(ctx context.Context, key string, u domain.User, id string, in Request) (jobs.Job, error) {
	admin := in.Operation == "autostart" || in.Operation == "uninstall" || in.Emergency
	if err := activeUser(u, admin); err != nil {
		return jobs.Job{}, err
	}
	if _, err := s.instances.Get(ctx, id); err != nil {
		return jobs.Job{}, err
	}
	switch in.Operation {
	case "install":
		if in.PreviewID == "" {
			return jobs.Job{}, domain.E(domain.CodeValidationFailed, "resolve an install preview before confirmation")
		}
	case "start", "stop", "restart", "autostart":
	case "uninstall":
		if in.Confirmation != id {
			return jobs.Job{}, domain.E(domain.CodeConflict, "type the instance id to confirm uninstall; worlds and retained executable generations are preserved")
		}
	default:
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "unknown lifecycle operation")
	}
	if in.Emergency && (in.Operation != "stop" && in.Operation != "restart" || in.Confirmation != id) {
		return jobs.Job{}, domain.E(domain.CodeConflict, "emergency stop requires explicit instance id confirmation")
	}
	if in.CountdownSeconds != nil && (*in.CountdownSeconds < 0 || *in.CountdownSeconds > 3600) {
		return jobs.Job{}, domain.E(domain.CodeValidationFailed, "countdown must be between 0 and 3600 seconds")
	}
	if (in.Operation == "stop" || in.Operation == "uninstall") && s.stopIntent != nil {
		if err := s.stopIntent(ctx, id, u); err != nil {
			return jobs.Job{}, err
		}
	}
	if err := s.audit(ctx, u, id, in.Operation); err != nil {
		return jobs.Job{}, fmt.Errorf("audit lifecycle request: %w", err)
	}
	raw, err := json.Marshal(jobInput{in, u.ID})
	if err != nil {
		return jobs.Job{}, err
	}
	request := jobs.Request{InstanceID: id, Kind: "lifecycle", Payload: raw, RequestedBy: u.ID, Cancellable: in.Operation != "uninstall"}
	if key != "" {
		return s.queue.EnqueueIdempotent(ctx, key, request)
	}
	return s.queue.Enqueue(ctx, request)
}
func (s *Service) run(ctx context.Context, e *jobs.Execution) (err error) {
	defer func() {
		if err != nil {
			s.recordFailure(e, err)
		}
	}()
	var in jobInput
	if json.Unmarshal(e.Payload, &in) != nil || in.ActorID <= 0 {
		return domain.E(domain.CodeValidationFailed, "invalid private lifecycle job")
	}
	if err := s.actor(ctx, in.ActorID, in.Operation == "autostart" || in.Operation == "uninstall" || in.Emergency); err != nil {
		return err
	}
	switch in.Operation {
	case "install":
		return s.install(ctx, e, in)
	case "start":
		return s.start(ctx, e)
	case "stop":
		return s.stop(ctx, e, in.Request)
	case "restart":
		if err := s.stop(ctx, e, in.Request); err != nil {
			return err
		}
		return s.start(ctx, e)
	case "autostart":
		return s.autostart(ctx, e, in.Enabled)
	case "uninstall":
		return s.uninstall(ctx, e, in.Confirmation)
	default:
		return domain.E(domain.CodeValidationFailed, "unknown private lifecycle operation")
	}
}
func (s *Service) previewPlan(ctx context.Context, id, previewID string, d instance.Detail) (domain.InstallPlan, error) {
	var raw, revision, original, expires string
	err := s.db.QueryRowContext(ctx, `SELECT plan_json,revision,original_selection_json,expires_at FROM lifecycle_previews WHERE id=? AND instance_id=?`, previewID, id).Scan(&raw, &revision, &original, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.InstallPlan{}, domain.NotFound("install preview")
	}
	if err != nil {
		return domain.InstallPlan{}, err
	}
	expiry, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil || !expiry.After(time.Now()) || revision != d.Instance.UpdatedAt.Format(time.RFC3339Nano) {
		return domain.InstallPlan{}, domain.E(domain.CodeConflict, "install preview expired or instance changed; resolve again")
	}
	var plan domain.InstallPlan
	var prior domain.VersionSelection
	if json.Unmarshal([]byte(original), &prior) != nil || prior != selection(d) {
		return plan, domain.E(domain.CodeConflict, "original selection changed since preview")
	}
	if json.Unmarshal([]byte(raw), &plan) != nil {
		return plan, domain.E(domain.CodeConflict, "install preview no longer matches the exact instance selection")
	}
	if err = s.allowSelection(ctx, d, plan.Selection); err != nil {
		return plan, err
	}
	if plan.Requirements.JavaMajor != d.Instance.JavaMajor {
		return plan, domain.E(domain.CodeConflict, "preview Java selection changed")
	}
	return plan, nil
}
func (s *Service) install(ctx context.Context, e *jobs.Execution, in jobInput) error {
	if err := e.Phase(ctx, "installing", 5); err != nil {
		return err
	}
	return s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
		d, err := session.Detail(ctx)
		if err != nil {
			return err
		}
		id := d.Instance.ID
		if err = s.recoverActivation(ctx, id); err != nil {
			return safeFailure("activation_recovery")
		}
		plan, err := s.previewPlan(ctx, id, in.PreviewID, d)
		if err != nil {
			return err
		}
		provider := s.providers[plan.Selection.Flavor]
		if provider == nil || s.runtime == nil {
			return domain.E(domain.CodeUnavailable, "managed distribution/runtime service unavailable")
		}
		if err = e.Phase(ctx, "managed_java", 10); err != nil {
			return err
		}
		var managed domain.JavaRuntime
		if old, oldErr := s.loadInstallation(ctx, id); oldErr == nil {
			managed, err = s.runtime.Get(ctx, old.Spec.JavaRuntimeID)
		} else if domain.AsError(oldErr).Code == domain.CodeInstanceNotInstalled {
			managed, err = s.runtime.EnsureRequirements(ctx, plan.Requirements, io.Discard)
		} else {
			return oldErr
		}
		if err != nil {
			return safeFailure("managed_java_verification")
		}
		if managed.Major < plan.Requirements.MinJavaMajor || (plan.Requirements.MaxJavaMajor != 0 && managed.Major > plan.Requirements.MaxJavaMajor) {
			return domain.E(domain.CodeConflict, "managed Java does not satisfy exact launch requirements")
		}
		if err = session.Materialize(ctx); err != nil {
			return safeFailure("materialization")
		}
		if err = e.Phase(ctx, "verified_download", 30); err != nil {
			return err
		}
		spec, err := provider.Install(ctx, id, plan, managed, io.Discard)
		if err != nil {
			return safeFailure("verified_distribution_install")
		}
		spec, err = bindMemory(spec, d.Instance.MemoryMiB)
		if err != nil {
			return err
		}
		generation, err := key()
		if err != nil {
			return err
		}
		if err = e.Phase(ctx, "activating", 85); err != nil {
			return err
		}
		if err = s.activate(ctx, id, installation{Generation: generation, Plan: plan, Spec: spec, InitialLoader: d.Instance.LoaderVersion == "" && plan.Selection.LoaderVersion != "", OriginalRevision: d.Instance.UpdatedAt.Format(time.RFC3339Nano)}); err != nil {
			return safeFailure("activation")
		}
		return e.Log(ctx, "Verified executable activation installed. EULA acceptance and fresh game readiness are separate requirements.")
	})
}
func (s *Service) activate(ctx context.Context, id string, next installation) error {
	if err := s.intent(ctx, id, next); err != nil {
		return err
	}
	if err := launcher.MarkActivationPending(ctx, s.dataDir, id); err != nil {
		return err
	}
	if err := s.seal(ctx, id, next.Spec); err != nil {
		return err
	}
	if err := s.saveInstallation(ctx, id, next); err != nil {
		return err
	}
	if err := launcher.ClearActivationPending(ctx, s.dataDir, id); err != nil {
		return err
	}
	return s.clearIntent(ctx, id)
}
func (s *Service) prepareLaunch(ctx context.Context, e *jobs.Execution) (domain.LaunchSpec, string, error) {
	var spec domain.LaunchSpec
	var instanceID string
	err := s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
		d, err := session.Detail(ctx)
		if err != nil {
			return err
		}
		instanceID = d.Instance.ID
		if !d.EULA.Accepted {
			return domain.E(domain.CodeConflict, "explicit Minecraft EULA acceptance is required before start")
		}
		if err = s.recoverActivation(ctx, d.Instance.ID); err != nil {
			return safeFailure("activation_recovery")
		}
		in, err := s.loadInstallation(ctx, d.Instance.ID)
		if err != nil {
			return err
		}
		if in.Plan.Selection != selection(d) {
			return domain.E(domain.CodeConflict, "installed selection differs from instance; strict update workflow required")
		}
		if _, modern := instance.ManagementProtocol(d.Instance.MinecraftVersion); modern == nil {
			if _, err = session.EnsureManagement(ctx); err != nil {
				return safeFailure("management_configuration")
			}
		} else if err = session.Materialize(ctx); err != nil {
			return safeFailure("materialization")
		}
		spec, err = bindMemory(in.Spec, d.Instance.MemoryMiB)
		if err != nil { // Previously bound memory may change through typed instance config.
			base, e := stripBoundMemory(in.Spec)
			if e != nil {
				return err
			}
			spec, err = bindMemory(base, d.Instance.MemoryMiB)
			if err != nil {
				return err
			}
		}
		generation, err := key()
		if err != nil {
			return err
		}
		in.InitialLoader = false
		in.OriginalRevision = ""
		in.Generation = generation
		in.Spec = spec
		return s.activate(ctx, d.Instance.ID, in)
	})
	return spec, instanceID, err
}
func (s *Service) start(ctx context.Context, e *jobs.Execution) error {
	return s.startGuarded(ctx, e, nil)
}
func (s *Service) startGuarded(ctx context.Context, e *jobs.Execution, guard RecoveryStartGuard) error {
	if err := e.Phase(ctx, "preparing_start", 10); err != nil {
		return err
	}
	spec, instanceID, err := s.prepareLaunch(ctx, e)
	if err != nil {
		return err
	}

	if err = e.Phase(ctx, "starting_process", 40); err != nil {
		return err
	}
	var started domain.ProcessIdentity
	err = e.WithInstanceMutation(ctx, func(id string) error {
		start := func() (domain.ProcessIdentity, error) { return s.native.Start(ctx, id, spec) }
		var err error
		if guard != nil {
			started, err = guard(ctx, id, start)
		} else {
			started, err = start()
		}
		return err
	})
	if err != nil {
		return s.startFailure(e, instanceID, "Native start failed; JVM state requires inspection.")
	}
	if err = e.Phase(ctx, "verifying_game_readiness", 65); err != nil {
		return s.startFailure(e, instanceID, "JVM started but readiness verification was interrupted.")
	}
	if s.control == nil {
		return s.startFailure(e, instanceID, "JVM started but native game readiness service is unavailable.")
	}
	err = s.verifyReady(ctx, e, started)
	if err != nil {
		return s.startFailure(e, instanceID, "JVM start did not reach acknowledged game readiness and required settings verification.")
	}
	return e.Log(ctx, "JVM running; fresh native game readiness and required settings readback verified. Gameplay overrides remain unverified.")
}
func stripBoundMemory(spec domain.LaunchSpec) (domain.LaunchSpec, error) {
	args, err := launchargs.StripMemory(spec.Args)
	spec.Args = args
	return spec, err
}
func (s *Service) startFailure(e *jobs.Execution, id, message string) error {
	ctx, cancel := e.CleanupContext()
	defer cancel()
	running := " Current process state is unknown."
	// Observation is read-only and must remain possible after job cancellation
	// has closed mutation admission. id was captured from the stopped session.
	identity, err := s.native.Identity(ctx, id)
	if os.IsNotExist(err) {
		running = " JVM is not running."
	} else if err == nil {
		observed, err := s.native.Inspect(ctx, identity)
		if err == nil {
			if observed.ProcessRunning {
				running = " JVM remains running; inspect console and stop explicitly if needed."
			} else {
				running = " JVM is not running."
			}
		}
	}

	return domain.E(domain.CodeUnavailable, message+running)
}
func (s *Service) stop(ctx context.Context, e *jobs.Execution, in Request) error {
	delay := 300
	if in.CountdownSeconds != nil {
		delay = *in.CountdownSeconds
	}
	if delay < 0 || delay > 3600 {
		return domain.E(domain.CodeValidationFailed, "invalid stop countdown")
	}
	if err := e.Phase(ctx, "stop_warning", 10); err != nil {
		return err
	}
	if !in.Emergency {
		if s.control == nil {
			return domain.E(domain.CodeUnavailable, "verified player and announcement control unavailable; an administrator may explicitly confirm emergency stop")
		}
		if err := s.control.WithJobSession(ctx, e, func(session *console.Session) error {
			observed, err := session.ObservePlayers(ctx)
			if err != nil {
				return err
			}
			if !observed.Known {
				return domain.E(domain.CodeUnavailable, "fresh player observation is unknown")
			}
			remaining := delay
			for {
				message := "Server stopping now."
				if remaining > 0 {
					message = fmt.Sprintf("Server stopping in %d seconds.", remaining)
				}
				result, err := session.Announce(ctx, message)
				if err != nil {
					return err
				}
				if !result.Verified {
					return domain.E(domain.CodeUnavailable, "stop announcement was not acknowledged")
				}
				if remaining == 0 {
					break
				}
				step := min(remaining, 60)
				timer := time.NewTimer(time.Duration(step) * time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				remaining -= step
			}
			observed, err = session.ObservePlayers(ctx)
			if err != nil {
				return err
			}
			if !observed.Known {
				return domain.E(domain.CodeUnavailable, "final player observation is unknown")
			}
			return nil
		}); err != nil {
			return domain.E(domain.CodeUnavailable, "Stop countdown could not be verified; process left running. Explicit administrator emergency confirmation is available.")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Phase(ctx, "stopping_process", 70); err != nil {
		return err
	}
	// Once stop is accepted, finish native bookkeeping under independent cleanup
	// time even if the cancellable countdown job context is cancelled.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 130*time.Second)
	defer cancel()
	return e.WithInstanceMutation(cleanup, func(id string) error {
		if in.Emergency && in.Confirmation != id {
			return domain.E(domain.CodeConflict, "emergency confirmation no longer matches instance")
		}
		identity, err := s.native.Identity(cleanup, id)
		if err != nil {
			return safeFailure("process_identity")
		}
		if err = s.native.Stop(cleanup, identity); err != nil {
			return domain.E(domain.CodeUnavailable, "Native stop did not complete cleanly; inspect current process state and launcher recovery details.")
		}
		return nil
	})
}
func (s *Service) autostart(ctx context.Context, e *jobs.Execution, enabled bool) error {
	if err := e.Phase(ctx, "setting_autostart", 30); err != nil {
		return err
	}
	if enabled {
		// A stopped instance needs the same current memory, management settings and
		// sealed launch used by manual start before native boot policy is enabled.
		var running bool
		if err := e.WithInstanceMutation(ctx, func(id string) error {
			identity, err := s.native.Identity(ctx, id)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return safeFailure("autostart_process_observation")
			}
			observed, err := s.native.Inspect(ctx, identity)
			if err != nil {
				return safeFailure("autostart_process_observation")
			}
			running = observed.ProcessRunning
			return nil
		}); err != nil {
			return err
		}
		if !running {
			if _, _, err := s.prepareLaunch(ctx, e); err != nil {
				return err
			}
		}
	}
	return e.WithInstanceMutation(ctx, func(id string) error {
		if enabled {
			d, err := s.instances.Get(ctx, id)
			if err != nil {
				return err
			}
			if !d.EULA.Accepted {
				return domain.E(domain.CodeConflict, "EULA acceptance required for autostart")
			}
			if _, err = s.loadInstallation(ctx, id); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO lifecycle_autostart(instance_id,desired,applied,error) VALUES(?,?,NULL,'Native application pending') ON CONFLICT(instance_id) DO UPDATE SET desired=excluded.desired,applied=NULL,error=excluded.error`, id, enabled); err != nil {
			return err
		}
		err := s.native.SetAutostart(ctx, id, enabled)
		cleanup, cancel := e.CleanupContext()
		defer cancel()
		if err != nil {
			_, persist := s.db.ExecContext(cleanup, `UPDATE lifecycle_autostart SET applied=NULL,error='Native setting failed; actual native policy is unknown' WHERE instance_id=?`, id)
			return errors.Join(domain.E(domain.CodeUnavailable, "native autostart setting failed; actual policy is unknown"), persist)
		}
		_, err = s.db.ExecContext(cleanup, `UPDATE lifecycle_autostart SET applied=?,error='' WHERE instance_id=?`, enabled, id)
		return err
	})
}
func (s *Service) uninstall(ctx context.Context, e *jobs.Execution, confirmation string) error {
	if err := s.autostart(ctx, e, false); err != nil {
		return err
	}
	if err := e.Phase(ctx, "removing_activation", 60); err != nil {
		return err
	}
	return s.instances.WithStoppedJob(ctx, e, func(session *instance.StoppedSession) error {
		d, err := session.Detail(ctx)
		if err != nil {
			return err
		}
		id := d.Instance.ID
		if confirmation != id {
			return domain.E(domain.CodeConflict, "uninstall requires instance id confirmation")
		}
		if err = s.recoverActivation(ctx, id); err != nil {
			return err
		}
		if err = s.intent(ctx, id, installation{Generation: "uninstalled"}); err != nil {
			return err
		}
		if err = launcher.MarkActivationPending(ctx, s.dataDir, id); err != nil {
			return err
		}
		// DB pointer removal is the commit boundary. Retained immutable generations
		// preserve rollback references; only activation is removed afterward.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `DELETE FROM lifecycle_installations WHERE instance_id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE instances SET java_runtime_id='' WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE instance_settings SET provisioning='not_installed' WHERE instance_id=?`, id); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if err = s.removeActivation(ctx, id); err != nil {
			return err
		}
		if err = launcher.ClearActivationPending(ctx, s.dataDir, id); err != nil {
			return err
		}
		if err = s.clearIntent(ctx, id); err != nil {
			return err
		}
		return e.Log(ctx, "Uninstalled active launch. Worlds, configuration, content, backups, managed runtime and immutable executable generations retained.")
	})
}

// AutostartPlans is called only after Windows prior processes stopped and paused
// queue/instance/activation reconciliation. Conflicting queued work blocks boot.
func (s *Service) AutostartPlans(ctx context.Context) (map[string]domain.LaunchSpec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT instance_id FROM lifecycle_autostart WHERE desired=1 AND applied=1 ORDER BY instance_id`)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	out := map[string]domain.LaunchSpec{}
	for _, id := range ids {
		var active int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE instance_id=? AND state IN('queued','running')`, id).Scan(&active); err != nil {
			return nil, err
		}
		if active > 0 {
			continue
		}
		d, err := s.instances.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if !d.EULA.Accepted {
			continue
		}
		in, err := s.loadInstallation(ctx, id)
		if err != nil {
			return nil, err
		}
		if in.Plan.Selection != selection(d) {
			return nil, domain.E(domain.CodeConflict, "autostart selection differs from installation")
		}
		if err = s.native.WithStopped(ctx, id, func() error {
			base, e := stripBoundMemory(in.Spec)
			if e != nil {
				return e
			}
			spec, e := bindMemory(base, d.Instance.MemoryMiB)
			if e != nil {
				return e
			}
			generation, e := key()
			if e != nil {
				return e
			}
			in.Spec = spec
			in.Generation = generation
			in.InitialLoader = false
			in.OriginalRevision = ""
			return s.activate(ctx, id, in)
		}); err != nil {
			return nil, safeFailure("autostart_verification")
		}
		out[id] = in.Spec
	}
	return out, nil
}

// Job completion intentionally hides provider errors. Persist only typed,
// authored domain messages without causes so users still see safe next steps.
func (s *Service) recordFailure(e *jobs.Execution, err error) {
	message := "Lifecycle operation failed; inspect its phase and current process state before retrying."
	if authored, ok := err.(*domain.Error); ok && authored.Cause == nil && authored.Code != domain.CodeInternal {
		message = authored.Message
	}
	ctx, cancel := e.CleanupContext()
	defer cancel()
	_ = e.Log(ctx, message)
}
