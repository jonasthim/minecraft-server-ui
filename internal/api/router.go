// Package api wires resource handlers and serves the embedded browser application.
package api

import (
	"context"
	"database/sql"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

type Dependencies struct {
	DB     *sql.DB
	Logger *slog.Logger
	Web    fs.FS
	Mount  func(chi.Router)
}

func NewRouter(d Dependencies) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if d.DB == nil || d.DB.PingContext(ctx) != nil {
			WriteError(w, domain.E(domain.CodeUnavailable, "Database unavailable"))
			return
		}
		WriteJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{"ok"})
	})
	if d.Mount != nil {
		d.Mount(r)
	}
	r.NotFound(spaHandler(d.Web))
	return r
}
