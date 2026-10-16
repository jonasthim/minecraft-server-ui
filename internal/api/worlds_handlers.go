package api

import (
	"log/slog"
	"mime"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

// MountWorlds belongs inside the shared authenticated /api/v1 group. Export
// uses the established GET token scope semantics plus operator role admission.
func MountWorlds(r chi.Router, s *world.Service) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/world", func(w http.ResponseWriter, r *http.Request) {
		d, e := s.Discover(r.Context(), chi.URLParam(r, "instanceId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, struct {
			World world.Discovery `json:"world"`
		}{d})
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Get("/instances/{instanceId}/world/download", func(w http.ResponseWriter, r *http.Request) {
		a, e := s.Capture(r.Context(), *auth.UserFrom(r.Context()), chi.URLParam(r, "instanceId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		defer func() {
			if e := a.Close(); e != nil {
				slog.Error("cleanup staged world export", "err", e)
			}
		}()
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Filename}))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Archive-SHA256", a.SHA256)
		w.Header().Set("X-World-Consistency", "best-effort-live-copy")
		controller := http.NewResponseController(w)
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Minute))
		defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
		http.ServeContent(w, r, a.Filename, time.Time{}, a)
	})
}
