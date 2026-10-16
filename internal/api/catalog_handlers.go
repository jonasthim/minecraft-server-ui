package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// MountCatalog belongs in the shared authenticated, CSRF-protected /api/v1 group.
func MountCatalog(r chi.Router, s *catalog.Service) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/catalog/providers", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Providers(r.Context())
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, struct {
			Providers []catalog.Availability `json:"providers"`
		}{v})
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Put("/catalog/curseforge-key", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Key string `json:"key"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		if e := DecodeJSON(r, &in); e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.ConfigureCurseForge(r.Context(), *auth.UserFrom(r.Context()), in.Key)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/catalog/{provider}/search", func(w http.ResponseWriter, r *http.Request) {
		q, e := catalogQuery(r)
		if e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.Search(r.Context(), q)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/catalog/{provider}/projects/{projectId}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Project(r.Context(), catalog.Provider(chi.URLParam(r, "provider")), chi.URLParam(r, "projectId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/catalog/{provider}/projects/{projectId}/versions", func(w http.ResponseWriter, r *http.Request) {
		q, e := catalogQuery(r)
		if e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.Versions(r.Context(), q.Provider, chi.URLParam(r, "projectId"), q)
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/catalog/{provider}/projects/{projectId}/versions/{versionId}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Version(r.Context(), catalog.Provider(chi.URLParam(r, "provider")), chi.URLParam(r, "projectId"), chi.URLParam(r, "versionId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
}
func catalogQuery(r *http.Request) (catalog.Query, error) {
	v := r.URL.Query()
	q := catalog.Query{Provider: catalog.Provider(chi.URLParam(r, "provider")), Kind: catalog.Kind(v.Get("kind")), Text: v.Get("q"), Minecraft: v.Get("minecraft"), Loader: v.Get("loader")}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"offset", &q.Offset}, {"limit", &q.Limit}} {
		if v.Has(p.name) {
			n, e := strconv.Atoi(v.Get(p.name))
			if e != nil {
				return q, domain.E(domain.CodeValidationFailed, "catalog page must use integers")
			}
			*p.dst = n
		}
	}
	return q, nil
}
