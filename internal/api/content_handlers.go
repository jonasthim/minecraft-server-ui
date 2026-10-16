package api

import (
	"bytes"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io"
	"net/http"
	"time"
)

// MountContent belongs in the shared authenticated, CSRF-protected /api/v1 group.
func MountContent(router chi.Router, service *content.Service) {
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/content", func(w http.ResponseWriter, r *http.Request) {
		v, e := service.Snapshot(r.Context(), chi.URLParam(r, "instanceId"))
		contentResponse(w, v, e)
	})
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/content/config", func(w http.ResponseWriter, r *http.Request) {
		v, e := service.ConfigList(r.Context(), chi.URLParam(r, "instanceId"))
		contentResponse(w, v, e)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Get("/instances/{instanceId}/content/config/text", func(w http.ResponseWriter, r *http.Request) {
		v, e := service.ConfigRead(r.Context(), chi.URLParam(r, "instanceId"), r.URL.Query().Get("path"), *auth.UserFrom(r.Context()))
		contentResponse(w, v, e)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post("/instances/{instanceId}/content/uploads", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, content.MaxArtifactBytes+1)
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
		v, e := service.StageUpload(r.Context(), chi.URLParam(r, "instanceId"), r.URL.Query().Get("filename"), r.Body, *auth.UserFrom(r.Context()))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusCreated, v)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/content/previews", func(w http.ResponseWriter, r *http.Request) {
		var input content.Mutation
		if e := decodeContentMutation(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := service.Preview(r.Context(), chi.URLParam(r, "instanceId"), input, *auth.UserFrom(r.Context()))
			contentResponse(w, v, e)
		})
		if input.UploadID != "" || input.Trial {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/content/previews/{previewId}/apply", func(w http.ResponseWriter, r *http.Request) {
		adminRequired, e := service.PreviewNeedsAdministrator(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), auth.UserFrom(r.Context()).ID)
		if e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := service.Enqueue(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), *auth.UserFrom(r.Context()))
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, http.StatusAccepted, map[string]any{"job": v})
		})
		if adminRequired {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Delete("/instances/{instanceId}/content/previews/{previewId}", func(w http.ResponseWriter, r *http.Request) {
		e := service.Discard(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), *auth.UserFrom(r.Context()))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusNoContent, nil)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Delete("/instances/{instanceId}/content/uploads", func(w http.ResponseWriter, r *http.Request) {
		e := service.Discard(r.Context(), chi.URLParam(r, "instanceId"), "", *auth.UserFrom(r.Context()))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusNoContent, nil)
	})
}
func contentResponse(w http.ResponseWriter, v any, e error) {
	if e != nil {
		WriteError(w, e)
		return
	}
	WriteJSON(w, http.StatusOK, v)
}
func decodeContentMutation(r *http.Request, v *content.Mutation) error {
	b, e := io.ReadAll(io.LimitReader(r.Body, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return domain.E(domain.CodeValidationFailed, "content preview request exceeds limit")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if e = dec.Decode(v); e != nil {
		return domain.E(domain.CodeValidationFailed, "invalid content preview request")
	}
	if dec.Decode(new(any)) != io.EOF {
		return domain.E(domain.CodeValidationFailed, "content request must contain one JSON value")
	}
	return nil
}
