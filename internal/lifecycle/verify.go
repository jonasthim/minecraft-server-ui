package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

type verifyInput struct {
	Identity domain.ProcessIdentity `json:"identity"`
}

// VerifyRunningAfterReconcile queues private rule verification for the exact
// healthy JVMs observed during startup. It never starts or stops a process.
// Existing queued work runs first; a changed generation refuses verification.
func (s *Service) VerifyRunningAfterReconcile(ctx context.Context) error {
	all, err := s.instances.List(ctx)
	if err != nil {
		return err
	}
	for _, detail := range all {
		id := detail.Instance.ID
		if s.deferAutostart != nil {
			hold, err := s.deferAutostart(ctx, id)
			if err != nil {
				return err
			}
			if hold {
				continue
			}
		}
		identity, err := s.native.Identity(ctx, id)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("observe startup identity %s: %w", id, err)
		}
		observed, err := s.native.Inspect(ctx, identity)
		if err != nil {
			return fmt.Errorf("observe startup process %s: %w", id, err)
		}
		if !observed.ProcessRunning || observed.State != domain.StateRunning || observed.WatchdogError != "" {
			continue
		}
		payload, err := json.Marshal(verifyInput{Identity: identity})
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,instance_id,target,details_json) VALUES(?,NULL,'system','lifecycle.verify.request',?,?,'{}')`, stamp(), id, id); err != nil {
			return fmt.Errorf("audit startup verification: %w", err)
		}
		if _, err = s.queue.Enqueue(ctx, jobs.Request{InstanceID: id, Kind: "lifecycle_verify", Payload: payload, RequestedBy: 0, Cancellable: true}); err != nil {
			return fmt.Errorf("queue startup verification %s: %w", id, err)
		}
	}
	return nil
}

func (s *Service) runVerify(ctx context.Context, e *jobs.Execution) (err error) {
	defer func() {
		if err != nil {
			s.recordFailure(e, err)
		}
	}()
	var in verifyInput
	if json.Unmarshal(e.Payload, &in) != nil || in.Identity.InstanceID == "" || in.Identity.PID <= 0 {
		return domain.E(domain.CodeValidationFailed, "invalid private readiness verification")
	}
	if err := e.WithInstanceMutation(ctx, func(id string) error {
		if id != in.Identity.InstanceID {
			return domain.E(domain.CodeForbidden, "readiness verification belongs to a different instance")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := e.Phase(ctx, "verifying_game_readiness", 30); err != nil {
		return err
	}
	if err := s.verifyReady(ctx, e, in.Identity); err != nil {
		return domain.E(domain.CodeUnavailable, "Startup game readiness and required settings could not be verified; inspect current process and console state.")
	}
	return e.Log(ctx, "Existing JVM generation verified with fresh game readiness and required native settings readback. Gameplay overrides remain unverified.")
}

func (s *Service) verifyReady(ctx context.Context, e *jobs.Execution, expected domain.ProcessIdentity) error {
	if s.control == nil {
		return domain.E(domain.CodeUnavailable, "native game readiness service is unavailable")
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	return s.control.WithJobSession(wait, e, func(session *console.Session) error {
		current, err := session.Identity(wait)
		if err != nil {
			return err
		}
		if current != expected {
			return domain.E(domain.CodeConflict, "JVM generation changed before readiness verification")
		}
		for {
			state, err := session.VerifyReady(wait)
			if err == nil && state.ReadyForUse {
				return nil
			}
			if err != nil && domain.AsError(err).Code == domain.CodeUnsupported {
				return err
			}
			timer := time.NewTimer(time.Second)
			select {
			case <-wait.Done():
				timer.Stop()
				return wait.Err()
			case <-timer.C:
			}
		}
	})
}
