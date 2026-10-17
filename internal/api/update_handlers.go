package api

import (
	"bytes"
	"encoding/json"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/update"
)

// MountUpdates belongs inside authenticated, CSRF-protected /api/v1 routing.
// RequireRole also enforces the existing read/operate/admin PAT scope caps.
func MountUpdates(router chi.Router, s *update.Service) {
	base := "/instances/{instanceId}/updates"
	router.With(auth.RequireRole(domain.RoleViewer)).Get(base, func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Status(r.Context(), chi.URLParam(r, "instanceId"))
		updateResponse(w, http.StatusOK, v, e)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Get(base+"/previews", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ListPreviews(r.Context(), chi.URLParam(r, "instanceId"), *auth.UserFrom(r.Context()))
		updateResponse(w, http.StatusOK, v, e)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Get(base+"/previews/{previewId}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.GetPreview(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), *auth.UserFrom(r.Context()))
		updateResponse(w, http.StatusOK, v, e)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post(base+"/previews", func(w http.ResponseWriter, r *http.Request) {
		var input update.Request
		if e := decodeUpdate(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		id := chi.URLParam(r, "instanceId")
		admin, e := s.PreviewNeedsAdministrator(r.Context(), id, input, auth.UserFrom(r.Context()).ID)
		if e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := s.Preview(r.Context(), id, input, *auth.UserFrom(r.Context()))
			updateResponse(w, http.StatusAccepted, map[string]any{"job": v}, e)
		})
		if admin {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	router.With(auth.RequireRole(domain.RoleOperator)).Post(base+"/previews/{previewId}/apply", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Confirmed bool `json:"confirmed"`
		}
		if e := decodeUpdate(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		id, preview := chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId")
		admin, e := s.ApplyNeedsAdministrator(r.Context(), id, preview, auth.UserFrom(r.Context()).ID)
		if e != nil {
			WriteError(w, e)
			return
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			v, e := s.EnqueueApply(r.Context(), id, preview, input.Confirmed, *auth.UserFrom(r.Context()))
			updateResponse(w, http.StatusAccepted, map[string]any{"job": v}, e)
		})
		if admin {
			auth.RequireRole(domain.RoleAdmin)(handler).ServeHTTP(w, r)
			return
		}
		handler.ServeHTTP(w, r)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post(base+"/previews/{previewId}/pack/members", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, modpack.MaxFileBytes+1)
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Minute))
		v, e := s.StagePackMember(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), r.URL.Query().Get("path"), r.Body, *auth.UserFrom(r.Context()))
		updateResponse(w, http.StatusCreated, v, e)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post(base+"/rollbacks/previews", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			TransactionID string `json:"transaction_id"`
		}
		if e := decodeUpdate(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.PreviewRollback(r.Context(), chi.URLParam(r, "instanceId"), input.TransactionID, *auth.UserFrom(r.Context()))
		updateResponse(w, http.StatusOK, v, e)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Post(base+"/rollbacks/{previewId}/apply", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Confirmation string `json:"confirmation"`
			Acknowledge  bool   `json:"acknowledge_world_rewind"`
		}
		if e := decodeUpdate(r, &input); e != nil {
			WriteError(w, e)
			return
		}
		v, e := s.EnqueueRollback(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "previewId"), input.Confirmation, input.Acknowledge, *auth.UserFrom(r.Context()))
		updateResponse(w, http.StatusAccepted, map[string]any{"job": v}, e)
	})
}
func updateResponse(w http.ResponseWriter, status int, v any, e error) {
	if e != nil {
		WriteError(w, e)
		return
	}
	WriteJSON(w, status, v)
}
func decodeUpdate(r *http.Request, input any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return domain.E(domain.CodeValidationFailed, "update request exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(input) != nil || decoder.Decode(new(any)) != io.EOF {
		return domain.E(domain.CodeValidationFailed, "invalid typed update request")
	}
	return nil
}
