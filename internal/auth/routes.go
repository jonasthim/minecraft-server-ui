package auth

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http"
	"strconv"
)

func (s *Service) audit(r *http.Request, action, target string) {
	s.recordAudit(r, action, "", target, nil)
}

func (s *Service) recordAudit(r *http.Request, action, instance, target string, details map[string]any) {
	e := domain.AuditEntry{Action: action, InstanceID: instance, Target: target, Details: RedactAuditDetails(details), IP: ClientIP(r)}
	if u := UserFrom(r.Context()); u != nil {
		e.UserID = &u.ID
		e.Username = u.Username
	}
	if err := db.NewAuditRepo(s.users.DB).Insert(r.Context(), e); err != nil {
		s.log.Warn("record audit entry", "action", action)
	}
}
func idParam(r *http.Request) (int64, error) {
	n, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if e != nil || n < 1 {
		return 0, domain.E(domain.CodeValidationFailed, "invalid id")
	}
	return n, nil
}
func response(w http.ResponseWriter, status int, v any, err error) {
	if err != nil {
		WriteError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	WriteJSON(w, status, v)
}
func (s *Service) mountRoutes(r chi.Router) {
	r.Get("/auth/status", func(w http.ResponseWriter, r *http.Request) {
		n, e := s.Count(r.Context())
		if e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.GetSettings(r.Context())
		response(w, 200, map[string]any{"needs_setup": n == 0, "local_login_enabled": v.Auth.LocalLoginEnabled, "oidc_enabled": v.Auth.OIDC.Enabled, "oidc_provider_name": v.Auth.OIDC.ProviderName}, e)
	})
	r.Post("/auth/setup", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username    string `json:"username"`
			Password    string `json:"password"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
		}
		if e := decode(r, &in); e != nil {
			WriteError(w, e)
			return
		}
		u, e := s.Setup(r.Context(), w, r, in.Username, in.Password, in.DisplayName, in.Email)
		if e == nil {
			s.audit(r, "auth.setup", u.Username)
		}
		response(w, 201, map[string]any{"user": u}, e)
	})
	r.Post("/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if e := decode(r, &in); e != nil {
			WriteError(w, e)
			return
		}
		u, e := s.Login(r.Context(), w, r, in.Username, in.Password)
		if e == nil {
			s.audit(r, "auth.login", u.Username)
		} else {
			s.audit(r, "auth.login.failed", "")
		}
		response(w, 200, map[string]any{"user": u}, e)
	})
	r.Get("/auth/oidc/login", s.OIDCLogin)
	r.Get("/auth/oidc/callback", s.OIDCCallback)
	r.Group(func(r chi.Router) {
		r.Use(RequireRole(domain.RoleViewer))
		r.Get("/auth/me", func(w http.ResponseWriter, r *http.Request) {
			WriteJSON(w, 200, map[string]any{"user": UserFrom(r.Context()), "csrf_token": s.csrfToken(r)})
		})
		r.Group(func(r chi.Router) {
			r.Use(browserOnly)
			r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
				s.audit(r, "auth.logout", "")
				s.Logout(r.Context(), w, r)
				w.WriteHeader(204)
			})
			r.Put("/auth/password", func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Current string `json:"current_password"`
					New     string `json:"new_password"`
				}
				if e := decode(r, &in); e != nil {
					WriteError(w, e)
					return
				}
				e := s.ChangePasswordFromRequest(r.Context(), r, UserFrom(r.Context()).ID, in.Current, in.New)
				if e == nil {
					s.audit(r, "user.password.change", "")
				}
				response(w, 204, nil, e)
			})
			r.Get("/auth/sessions", func(w http.ResponseWriter, r *http.Request) {
				v, e := s.ListSessions(r.Context(), r, UserFrom(r.Context()).ID)
				response(w, 200, map[string]any{"sessions": v}, e)
			})
			r.Delete("/auth/sessions/{sessionId}", func(w http.ResponseWriter, r *http.Request) {
				e := s.RevokeSession(r.Context(), UserFrom(r.Context()).ID, chi.URLParam(r, "sessionId"))
				if e == nil {
					s.audit(r, "auth.session.revoke", "")
				}
				response(w, 204, nil, e)
			})
			r.Post("/auth/sessions/revoke-others", func(w http.ResponseWriter, r *http.Request) {
				e := s.RevokeOtherSessions(r.Context(), r, UserFrom(r.Context()).ID)
				if e == nil {
					s.audit(r, "auth.session.revoke-others", "")
				}
				response(w, 204, nil, e)
			})
			r.Get("/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
				v, e := s.ListTokens(r.Context(), UserFrom(r.Context()).ID)
				response(w, 200, map[string]any{"tokens": v}, e)
			})
			r.Post("/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Name   string   `json:"name"`
					Days   int      `json:"expires_in_days"`
					Scopes []string `json:"scopes"`
				}
				if e := decode(r, &in); e != nil {
					WriteError(w, e)
					return
				}
				v, secret, e := s.CreateScopedToken(r.Context(), UserFrom(r.Context()), in.Name, in.Days, in.Scopes)
				if e == nil {
					s.audit(r, "token.create", v.Name)
				}
				response(w, 201, map[string]any{"token": v, "secret": secret}, e)
			})
			r.Delete("/auth/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
				id, e := idParam(r)
				if e == nil {
					e = s.RevokeToken(r.Context(), UserFrom(r.Context()).ID, id)
				}
				if e == nil {
					s.audit(r, "token.revoke", "")
				}
				response(w, 204, nil, e)
			})
			r.Group(func(r chi.Router) {
				r.Use(RequireRole(domain.RoleAdmin))
				r.Get("/users", func(w http.ResponseWriter, r *http.Request) {
					v, e := s.List(r.Context())
					response(w, 200, map[string]any{"users": v}, e)
				})
				r.Post("/users", func(w http.ResponseWriter, r *http.Request) {
					var in struct {
						Username    string      `json:"username"`
						Password    string      `json:"password"`
						DisplayName string      `json:"display_name"`
						Email       string      `json:"email"`
						Role        domain.Role `json:"role"`
					}
					if e := decode(r, &in); e != nil {
						WriteError(w, e)
						return
					}
					u, e := s.Create(r.Context(), in.Username, in.Password, in.DisplayName, in.Email, in.Role)
					if e == nil {
						s.audit(r, "user.create", u.Username)
					}
					response(w, 201, map[string]any{"user": u}, e)
				})
				r.Patch("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
					var in struct {
						DisplayName *string      `json:"display_name"`
						Email       *string      `json:"email"`
						Role        *domain.Role `json:"role"`
						Disabled    *bool        `json:"disabled"`
					}
					if e := decode(r, &in); e != nil {
						WriteError(w, e)
						return
					}
					id, e := idParam(r)
					if e != nil {
						WriteError(w, e)
						return
					}
					u, e := s.Update(r.Context(), id, in.DisplayName, in.Email, in.Role, in.Disabled)
					if e == nil {
						s.audit(r, "user.update", u.Username)
					}
					response(w, 200, map[string]any{"user": u}, e)
				})
				r.Delete("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
					id, e := idParam(r)
					if e == nil {
						e = s.Delete(r.Context(), id)
					}
					if e == nil {
						s.audit(r, "user.delete", strconv.FormatInt(id, 10))
					}
					response(w, 204, nil, e)
				})
				r.Put("/users/{id}/password", func(w http.ResponseWriter, r *http.Request) {
					var in struct {
						Password string `json:"password"`
					}
					if e := decode(r, &in); e != nil {
						WriteError(w, e)
						return
					}
					id, e := idParam(r)
					if e == nil {
						e = s.SetPassword(r.Context(), id, in.Password)
					}
					if e == nil {
						s.audit(r, "user.password.reset", strconv.FormatInt(id, 10))
					}
					response(w, 204, nil, e)
				})
				r.Get("/settings/auth", func(w http.ResponseWriter, r *http.Request) {
					v, e := s.GetSettings(r.Context())
					response(w, 200, v, e)
				})
				r.Put("/settings/auth", func(w http.ResponseWriter, r *http.Request) {
					var in domain.Settings
					if e := decode(r, &in); e != nil {
						WriteError(w, e)
						return
					}
					v, e := s.PutSettings(r.Context(), in)
					if e == nil {
						s.audit(r, "settings.auth.update", "")
					}
					response(w, 200, v, e)
				})
				r.Get("/audit", func(w http.ResponseWriter, r *http.Request) {
					before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
					v, e := db.NewAuditRepo(s.users.DB).List(r.Context(), r.URL.Query().Get("instance_id"), r.URL.Query().Get("username"), 100, before)
					response(w, 200, map[string]any{"entries": v}, e)
				})
			})
		})
	})
}

type serviceAuditor struct{ s *Service }

func (a serviceAuditor) Record(r *http.Request, action, instance, target string, details map[string]any) {
	a.s.recordAudit(r, action, instance, target, details)
}
