package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"mime"
	"net/http"
	"time"
)

// MountBackups belongs inside the authenticated, CSRF-protected API group.
// Metadata is viewer-visible; private archive exports and mutations require an
// operator. No uploaded archive or arbitrary host path endpoint exists.
func MountBackups(r chi.Router, s *backup.Service) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/backups/retention", func(w http.ResponseWriter, r *http.Request) {
		policy, err := s.GetRetentionPolicy(r.Context(), chi.URLParam(r, "instanceId"))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, policy)
	})
	r.With(auth.RequireRole(domain.RoleAdmin)).Put("/instances/{instanceId}/backups/retention", func(w http.ResponseWriter, r *http.Request) {
		// Every value is required: omitted or null input must never silently
		// disable a limit by becoming the Go integer zero value.
		var input struct {
			KeepLast         *int   `json:"keep_last"`
			KeepDays         *int   `json:"keep_days"`
			ExpectedRevision *int64 `json:"expected_revision"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		if input.KeepLast == nil || input.KeepDays == nil || input.ExpectedRevision == nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "keep_last, keep_days and expected_revision are required"))
			return
		}
		policy, err := s.SetRetentionPolicy(r.Context(), chi.URLParam(r, "instanceId"), backup.RetentionPolicyInput{
			KeepLast: *input.KeepLast, KeepDays: *input.KeepDays, ExpectedRevision: *input.ExpectedRevision,
		}, *auth.UserFrom(r.Context()))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, policy)
	})
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/backups", func(w http.ResponseWriter, r *http.Request) {
		records, err := s.List(r.Context(), chi.URLParam(r, "instanceId"))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, map[string]any{"backups": records})
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/backups", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Kind domain.BackupKind `json:"kind"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		job, err := s.Enqueue(r.Context(), chi.URLParam(r, "instanceId"), input.Kind, domain.BackupManual, *auth.UserFrom(r.Context()))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/backups/{backupId}/restore", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Confirmed bool `json:"confirmed"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		job, err := s.EnqueueRestore(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "backupId"), input.Confirmed, *auth.UserFrom(r.Context()))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Get("/instances/{instanceId}/backups/{backupId}/download", func(w http.ResponseWriter, r *http.Request) {
		download, err := s.Download(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "backupId"), *auth.UserFrom(r.Context()))
		if err != nil {
			WriteError(w, err)
			return
		}
		defer download.Close()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": download.Filename}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Archive-SHA256", download.SHA256)
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Minute))
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		http.ServeContent(w, r, download.Filename, time.Time{}, download.File)
	})
}
