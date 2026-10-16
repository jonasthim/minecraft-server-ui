package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"net/http"
)

// MountInstances belongs inside the authenticated /api/v1 group with CSRF.
func MountInstances(r chi.Router, s *instance.Service) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instance-settings/capabilities", func(w http.ResponseWriter, r *http.Request) {
		selection := domain.VersionSelection{Flavor: domain.Flavor(r.URL.Query().Get("flavor")), MinecraftVersion: r.URL.Query().Get("minecraft_version")}
		settings, err := instance.SettingsCapabilities(selection)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, map[string]any{"settings": settings})
	})

	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.List(r.Context())
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"instances": v})
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Get(r.Context(), chi.URLParam(r, "instanceId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"detail": v})
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Post("/instances", func(w http.ResponseWriter, r *http.Request) {
		var input instance.Draft
		if e := DecodeJSON(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.Create(r.Context(), *auth.UserFrom(r.Context()), input)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 201, map[string]any{"detail": v})
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Put("/instances/{instanceId}", func(w http.ResponseWriter, r *http.Request) {
		var input instance.Draft
		if e := DecodeJSON(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.Update(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"), input)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"detail": v})
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Delete("/instances/{instanceId}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Confirmation string `json:"confirmation"`
		}
		if e := DecodeJSON(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		if e := s.Delete(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"), input.Confirmation); e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 204, nil)
	})
}
