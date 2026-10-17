package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"io"
	"net/http"
)

type SelfUpdateService interface {
	Status(context.Context) selfupdate.Status
	Discover(context.Context, domain.User) (selfupdate.Discovery, error)
	Check(context.Context, string, domain.User) (selfupdate.CheckResult, error)
	Stage(context.Context, string, domain.User) (selfupdate.Stage, error)
	Apply(context.Context, selfupdate.ApplyInput, domain.User) error
}

// MountSelfUpdate belongs inside the existing Authenticate and CSRF group.
func MountSelfUpdate(r chi.Router, s SelfUpdateService) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleAdmin))
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.RawQuery != "" {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Manager update endpoints do not accept query parameters."))
					return
				}
				if s == nil {
					WriteError(w, domain.E(domain.CodeUnavailable, "Manager update service unavailable."))
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		r.Get("/system/update", func(w http.ResponseWriter, r *http.Request) { WriteJSON(w, 200, s.Status(r.Context())) })
		r.Post("/system/update/discover", func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				body, err := io.ReadAll(io.LimitReader(r.Body, 1))
				if err != nil || len(body) != 0 {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Release discovery does not accept a request body."))
					return
				}
			}
			out, err := s.Discover(r.Context(), *auth.UserFrom(r.Context()))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, out)
		})
		versionHandler := func(stage bool) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				var in struct {
					Version string `json:"version"`
				}
				if err := DecodeJSON(r, &in); err != nil {
					WriteError(w, err)
					return
				}
				if _, err := selfupdate.ParseStable(in.Version); err != nil {
					WriteError(w, domain.E(domain.CodeValidationFailed, "A stable vMAJOR.MINOR.PATCH release is required."))
					return
				}
				actor := *auth.UserFrom(r.Context())
				if stage {
					out, err := s.Stage(r.Context(), in.Version, actor)
					if err != nil {
						WriteError(w, err)
						return
					}
					WriteJSON(w, 200, out)
				} else {
					out, err := s.Check(r.Context(), in.Version, actor)
					if err != nil {
						WriteError(w, err)
						return
					}
					WriteJSON(w, 200, out)
				}
			}
		}
		r.Post("/system/update/check", versionHandler(false))
		r.Post("/system/update/stage", versionHandler(true))
		r.Post("/system/update/apply", func(w http.ResponseWriter, r *http.Request) {
			var in selfupdate.ApplyInput
			if err := DecodeJSON(r, &in); err != nil {
				WriteError(w, err)
				return
			}
			if err := s.Apply(r.Context(), in, *auth.UserFrom(r.Context())); err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 202, map[string]string{"status": "accepted"})
		})
	})
}
