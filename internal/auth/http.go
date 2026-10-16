package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

type Config struct {
	BaseURL         string
	InsecureCookies bool
}
type Auditor interface {
	Record(*http.Request, string, string, string, map[string]any)
}
type contextKey int

const (
	userKeyContext contextKey = iota
	tokenKeyContext
	scopesKeyContext
)

func WithUser(ctx context.Context, u *domain.User) context.Context {
	return context.WithValue(ctx, userKeyContext, u)
}
func UserFrom(ctx context.Context) *domain.User {
	u, _ := ctx.Value(userKeyContext).(*domain.User)
	return u
}
func WithTokenAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, tokenKeyContext, true)
}
func IsTokenAuth(ctx context.Context) bool { b, _ := ctx.Value(tokenKeyContext).(bool); return b }
func ClientIP(r *http.Request) string      { return remoteIP(r) }
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func WriteError(w http.ResponseWriter, err error) {
	e := domain.AsError(err)
	WriteJSON(w, e.HTTPStatus(), map[string]any{"error": map[string]any{"code": e.Code, "message": e.Message, "fields": e.Fields}})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return domain.E(domain.CodeValidationFailed, "invalid JSON request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return domain.E(domain.CodeValidationFailed, "request must contain one JSON value")
	}
	return nil
}
func RequireRole(role domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFrom(r.Context())
			if u == nil {
				WriteError(w, domain.E(domain.CodeUnauthorized, "authentication required"))
				return
			}
			if u.Disabled || !u.Role.AtLeast(role) {
				WriteError(w, domain.E(domain.CodeForbidden, "insufficient permissions"))
				return
			}
			if IsTokenAuth(r.Context()) {
				scopes, _ := r.Context().Value(scopesKeyContext).([]string)
				need := "read"
				if role == domain.RoleAdmin {
					need = "admin"
				} else if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
					need = "operate"
				}
				allowed := false
				for _, s := range scopes {
					if s == "admin" || s == need || s == "operate" && need == "read" {
						allowed = true
					}
				}
				if !allowed {
					WriteError(w, domain.E(domain.CodeForbidden, "token scope does not allow this operation"))
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
func browserOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsTokenAuth(r.Context()) {
			WriteError(w, domain.E(domain.CodeForbidden, "use a browser session to manage identity"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Service) csrfToken(r *http.Request) string {
	c, e := r.Cookie(CookieName)
	if e != nil {
		return ""
	}
	return HashToken("csrf:" + c.Value)
}

// CSRF rejects cross-origin unsafe requests, and requires a session-bound
// token for authenticated cookie writes. Bearer requests carry no ambient credential.
func (s *Service) CSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS" {
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			expected := r.Host
			expectedScheme := "http"
			if r.TLS != nil {
				expectedScheme = "https"
			}
			if s.cfg.BaseURL != "" {
				if b, e := url.Parse(s.cfg.BaseURL); e == nil {
					expected = b.Host
					expectedScheme = b.Scheme
				}
			}
			if err != nil || u.Host != expected || u.Scheme != expectedScheme || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
				WriteError(w, domain.E(domain.CodeForbidden, "cross-origin request rejected"))
				return
			}
		}
		if !IsTokenAuth(r.Context()) && UserFrom(r.Context()) != nil {
			want := s.csrfToken(r)
			got := r.Header.Get("X-CSRF-Token")
			if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
				WriteError(w, domain.E(domain.CodeForbidden, "invalid CSRF token"))
				return
			}
		} else if !IsTokenAuth(r.Context()) && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			WriteError(w, domain.E(domain.CodeForbidden, "JSON content type required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Service) Mount(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) { r.Use(s.Authenticate, s.CSRF); s.mountRoutes(r) })
}

// MountRoutes registers identity endpoints on a shared /api/v1 router. The
// caller installs Authenticate and CSRF once before adding feature routes.
func (s *Service) MountRoutes(r chi.Router) { s.mountRoutes(r) }
