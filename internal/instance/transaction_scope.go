package instance

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// WithTransactionScope retains the original stopped admission for an entire
// multi-participant callback, including drain of a callback started on another
// goroutine. Do not invoke another session method inside fn. Fetch every value
// needed from the service before opening a SQL transaction.
func (s *StoppedSession) WithTransactionScope(ctx context.Context, fn func(context.Context, Detail, Directories) error) error {
	if fn == nil {
		return domain.E(domain.CodeValidationFailed, "transaction callback is required")
	}
	scope, release, e := s.enter(ctx)
	if e != nil {
		return e
	}
	defer release()
	operation, cancel := sessionContext(ctx, scope.ctx)
	defer cancel()
	detail, e := scope.service.Get(operation, scope.id)
	if e != nil {
		return e
	}
	paths, e := scope.service.paths.For(scope.id)
	if e != nil {
		return e
	}
	return fn(operation, detail, paths)
}
