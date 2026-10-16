package api

import (
	"bytes"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"io"
	"net/http"
	"time"
)

// MountModpacks belongs inside authenticated, CSRF-protected /api/v1 routing.
func MountModpacks(router chi.Router, s *modpack.Service) {
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/modpack", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Snapshot(r.Context(), chi.URLParam(r, "instanceId"))
		modpackResponse(w, v, e)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/modpack/previews", func(w http.ResponseWriter, r *http.Request) {
		var input modpack.Request
		if e := decodeModpack(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := s.Preview(r.Context(), chi.URLParam(r, "instanceId"), input, *auth.UserFrom(r.Context()))
			modpackResponse(w, v, e)
		})
		needsAdmin, e := s.RequestNeedsAdministrator(r.Context(), chi.URLParam(r, "instanceId"), input, auth.UserFrom(r.Context()).ID)
		if e != nil {
			WriteError(w, e)
			return
		}
		if needsAdmin {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/modpack/previews/{previewId}/apply", func(w http.ResponseWriter, r *http.Request) {
		needsAdmin, e := s.PreviewNeedsAdministrator(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), auth.UserFrom(r.Context()).ID)
		if e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := s.Enqueue(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), *auth.UserFrom(r.Context()))
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, http.StatusAccepted, map[string]any{"job": v})
		})
		if needsAdmin {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})

	router.With(auth.RequireRole(domain.RoleOperator)).Delete("/instances/{instanceId}/modpack/previews/{previewId}", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Discard(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), *auth.UserFrom(r.Context())); e != nil {
			WriteError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post("/instances/{instanceId}/modpack/archives", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, modpack.MaxArchiveBytes+1)
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
		selection := catalog.Selection{Provider: catalog.Provider(r.URL.Query().Get("provider")), ProjectID: r.URL.Query().Get("project_id"), VersionID: r.URL.Query().Get("version_id"), FileID: r.URL.Query().Get("file_id")}
		v, e := s.StageArchive(r.Context(), chi.URLParam(r, "instanceId"), selection, r.Body, *auth.UserFrom(r.Context()))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusCreated, v)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post("/instances/{instanceId}/modpack/previews/{previewId}/members/resume", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, modpack.MaxFileBytes+1)
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
		v, e := s.ResumeMember(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), r.URL.Query().Get("member"), r.Body, *auth.UserFrom(r.Context()))
		modpackResponse(w, v, e)
	})
}
func modpackResponse(w http.ResponseWriter, v any, e error) {
	if e != nil {
		WriteError(w, e)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}
func decodeModpack(r *http.Request, v *modpack.Request) error {
	b, e := io.ReadAll(io.LimitReader(r.Body, 4<<20+1))
	if e != nil || len(b) > 4<<20 {
		return domain.E(domain.CodeValidationFailed, "pack preview input exceeds bound")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil || d.Decode(new(any)) != io.EOF {
		return domain.E(domain.CodeValidationFailed, "invalid pack preview input")
	}
	if v.Choices == nil || v.Selections == nil || v.Manual == nil {
		return domain.E(domain.CodeValidationFailed, "pack choices, selections and manual receipts must be objects")
	}
	if len(v.Choices) > 8192 || len(v.Selections) > 8192 || len(v.Manual) > 8192 {
		return domain.E(domain.CodeValidationFailed, "pack selection count exceeds bound")
	}
	return nil
}
