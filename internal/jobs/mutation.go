package jobs

import (
	"context"
	"errors"

	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// Immutable identity fields are created only by the dispatcher and bound to
// the exact original Execution pointer. Public Job metadata is diagnostic only.
// accepting/mutationDone/mutationErr are protected by Service.mu.
type executionIdentity struct {
	service           *Service
	owner             *Execution
	jobID, instanceID string
	ctx               context.Context
	accepting         bool
	mutationDone      chan struct{}
	mutationErr       error
}

func (s *Service) bindExecution(ctx context.Context, id string, job domain.Job, payload []byte) *Execution {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.running[id]
	identity := &executionIdentity{service: s, jobID: id, instanceID: current.instance, ctx: ctx, accepting: true}
	e := &Execution{Job: job, Payload: payload, service: s, secrets: payloadSecrets(payload), identity: identity}
	identity.owner = e
	current.execution = identity
	s.running[id] = current
	return e
}

func (e *Execution) journalIdentity() (string, *Service, error) {
	if e == nil || e.identity == nil || e.identity.owner != e || e.service != e.identity.service {
		return "", nil, domain.E(domain.CodeForbidden, "invalid job execution identity")
	}
	return e.identity.jobID, e.identity.service, nil
}

// WithInstanceMutation authorizes mutation of the private dispatch instance.
// It accepts only the original, currently running execution while its handler
// remains active; nil, forged, copied, stale and instance-less executions cannot
// acquire authority by changing public Job fields. Only one callback may run at
// once for the execution. The callback receives the authoritative instance ID.
//
// This retains the current job's queue slot and is not a native lock. Compose
// execution admission -> supervisor.WithStopped exactly once -> stopped instance
// session -> filesystem/DB mutation. Do not recursively acquire queue/native
// guards. Phase/Log remain usable; no global mutex or SQL connection is held
// during the callback. Capture ctx for I/O and return only after cleanup.
// Handler return closes further admission and drains an already admitted
// callback, including detached goroutines, before releasing the job's slot.
func (e *Execution) WithInstanceMutation(ctx context.Context, fn func(string) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, s, identityErr := e.journalIdentity()
	if identityErr != nil {
		return identityErr
	}
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "instance mutation callback is required")
	}
	identity := e.identity
	s.mu.Lock()
	current, ok := s.running[identity.jobID]
	if !ok || current.execution != identity || !identity.accepting {
		s.mu.Unlock()
		return domain.E(domain.CodeForbidden, "job execution mutation admission is closed")
	}
	if domain.ValidateInstanceID(identity.instanceID) != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeValidationFailed, "job execution has no valid instance")
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.stopping || s.ctx.Err() != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeUnavailable, "job service is stopping")
	}
	if err := identity.ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if identity.mutationDone != nil {
		s.mu.Unlock()
		return domain.E(domain.CodeInstanceBusy, "job instance mutation is already active")
	}
	done := make(chan struct{})
	identity.mutationDone = done
	identity.mutationErr = nil
	s.mu.Unlock()
	defer func() {
		panicValue := recover()
		s.mu.Lock()
		identity.mutationErr = err
		if panicValue != nil {
			identity.mutationErr = errors.New("instance mutation panicked")
		}
		identity.mutationDone = nil
		close(done)
		s.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := identity.ctx.Err(); err != nil {
		return err
	}
	return fn(identity.instanceID)
}

// Closing admission and capturing its active callback is one atomic step.
// Waiting has no cancellation shortcut: cancellation cannot free the slot
// while a filesystem/native mutation is still active. Stop reports its own
// deadline without pretending this worker has drained.
func (e *Execution) finishMutationAdmission() error {
	identity := e.identity
	s := identity.service
	s.mu.Lock()
	identity.accepting = false
	done := identity.mutationDone
	s.mu.Unlock()
	if done == nil {
		return nil
	}
	<-done
	s.mu.Lock()
	defer s.mu.Unlock()
	// If an improperly detached callback fails after the handler returns, the
	// job must not report success solely because that handler returned nil.
	return identity.mutationErr
}
