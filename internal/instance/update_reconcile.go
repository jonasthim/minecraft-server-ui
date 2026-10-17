package instance

import "context"

// WithReconcileHold defers only ordinary materialization for a read-only,
// coordinator-validated administrator hold. Deletion journals are still handled
// first and configuration journals are never silently skipped.
func WithReconcileHold(hold func(context.Context, string) (bool, error)) Option {
	return func(s *Service) { s.reconcileHold = hold }
}
