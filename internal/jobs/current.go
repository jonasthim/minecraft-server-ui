package jobs

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// CurrentExecution is immutable dispatcher identity, returned as diagnostic
// data. CheckCurrent validates authority but does not acquire mutation admission.
type CurrentExecution struct {
	JobID, InstanceID, Kind string
	RequesterID             int64
}

// CheckCurrent is safe inside WithInstanceMutation. It reads only private queue
// identity under a short mutex, holds no SQL connection and grants no new slot.
func (e *Execution) CheckCurrent(ctx context.Context) (CurrentExecution, error) {
	if err := ctx.Err(); err != nil {
		return CurrentExecution{}, err
	}
	_, s, err := e.journalIdentity()
	if err != nil {
		return CurrentExecution{}, err
	}
	identity := e.identity
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.running[identity.jobID]
	if !ok || current.execution != identity || !identity.accepting {
		return CurrentExecution{}, domain.E(domain.CodeForbidden, "original execution admission is closed")
	}
	if s.stopping || s.ctx.Err() != nil {
		return CurrentExecution{}, domain.E(domain.CodeUnavailable, "job service is stopping")
	}
	if err := identity.ctx.Err(); err != nil {
		return CurrentExecution{}, err
	}
	return CurrentExecution{JobID: identity.jobID, InstanceID: identity.instanceID, Kind: identity.kind, RequesterID: identity.requester}, nil
}
