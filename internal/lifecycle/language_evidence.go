package lifecycle

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/server/loaderutil"
)

// LanguageEvidence reads only the current committed immutable installation; a
// pending activation or tuple mismatch cannot supply compatibility evidence.
func (s *Service) LanguageEvidence(ctx context.Context, id string, selection domain.VersionSelection) (loaderutil.LanguageEvidence, error) {
	var count int
	if e := s.db.QueryRowContext(ctx, "SELECT count(*) FROM lifecycle_activations WHERE instance_id=?", id).Scan(&count); e != nil {
		return loaderutil.LanguageEvidence{}, e
	}
	if count > 0 {
		return loaderutil.LanguageEvidence{}, domain.E(domain.CodeConflict, "loader activation is pending")
	}
	in, e := s.loadInstallation(ctx, id)
	if e != nil {
		return loaderutil.LanguageEvidence{}, e
	}
	if in.Plan.Selection != selection {
		return loaderutil.LanguageEvidence{}, domain.E(domain.CodeConflict, "loader evidence tuple differs from current committed installation")
	}
	return loaderutil.VerifiedLanguageEvidence(ctx, s.dataDir, id, in.Spec)
}
