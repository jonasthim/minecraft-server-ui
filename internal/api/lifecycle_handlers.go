package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
	"net/http"
)

// MountLifecycle belongs in the authenticated, CSRF-protected /api/v1 group.
func MountLifecycle(r chi.Router, s *lifecycle.Service) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/lifecycle/catalog", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.Catalog(r.Context(), domain.Flavor(r.URL.Query().Get("flavor")), r.URL.Query().Get("minecraft_version"))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.State(r.Context(), chi.URLParam(r, "instanceId"))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/lifecycle/preview", func(w http.ResponseWriter, r *http.Request) {
		var in domain.VersionSelection
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, err)
			return
		}
		out, err := s.Preview(r.Context(), chi.URLParam(r, "instanceId"), in)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, out)
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		var in lifecycle.Request
		if err := DecodeJSON(r, &in); err != nil {
			WriteError(w, err)
			return
		}
		out, err := s.Enqueue(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"), in)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 202, map[string]any{"job": out})
	})
}
