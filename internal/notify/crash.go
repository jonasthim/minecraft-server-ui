package notify

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"regexp"
	"time"
)

var crashEventID = regexp.MustCompile(`^crash:[a-f0-9]{64}$`)

// EnqueueServerCrash consumes the authoritative scheduler's exact-generation
// exit proof as a bounded opaque ID. Process IDs, arguments, reason strings and
// native errors are intentionally absent from this producer capability.
func (s *Service) EnqueueServerCrash(ctx context.Context, id, instance string, occurred time.Time) error {
	if !crashEventID.MatchString(id) || domain.ValidateInstanceID(instance) != nil || occurred.IsZero() || occurred.After(s.cfg.Now()) {
		return domain.E(domain.CodeValidationFailed, "invalid authoritative crash event")
	}
	return s.Enqueue(ctx, Event{ID: id, InstanceID: instance, Kind: ServerCrashed, Operation: "crash_recovery", CreatedAt: occurred})
}
