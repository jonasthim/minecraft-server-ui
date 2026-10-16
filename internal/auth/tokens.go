package auth

import (
	"context"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http"
	"strings"
)

// Scopes are independent caps on the owner's current global role. Read permits
// GET, operate adds viewer/operator writes, admin allows administrator routes.
func (s *Service) CreateScopedToken(ctx context.Context, u *domain.User, name string, days int, scopes []string) (domain.APIToken, string, error) {
	if len(scopes) == 0 {
		scopes = []string{"read"}
	}
	for _, scope := range scopes {
		if scope != "read" && scope != "operate" && scope != "admin" {
			return domain.APIToken{}, "", domain.E(domain.CodeValidationFailed, "unknown token scope")
		}
		if scope == "admin" && u.Role != domain.RoleAdmin || scope == "operate" && !u.Role.AtLeast(domain.RoleOperator) {
			return domain.APIToken{}, "", domain.E(domain.CodeForbidden, "token scope exceeds your role")
		}
	}
	t, secret, e := s.CreateToken(ctx, u.ID, name, days)
	if e != nil {
		return t, "", e
	}
	doc, _ := json.Marshal(scopes)
	if _, e = s.tokens.DB.ExecContext(ctx, "UPDATE api_tokens SET scopes=? WHERE id=?", string(doc), t.ID); e != nil {
		_ = s.tokens.Delete(ctx, u.ID, t.ID)
		return domain.APIToken{}, "", e
	}
	t.Scopes = scopes
	return t, secret, nil
}
func (s *Service) tokenScopes(r *http.Request) []string {
	_, t, e := s.tokens.GetByHash(r.Context(), HashToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	if e != nil {
		return nil
	}
	return t.Scopes
}
