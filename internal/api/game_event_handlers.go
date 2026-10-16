package api

import (
	"context"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events/game"
)

type GameEvents interface {
	History(context.Context, string, int64, int) (game.Page, error)
}

// MountGameEvents mounts only viewer reads inside the authenticated API group.
func MountGameEvents(r chi.Router, s GameEvents) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/game-events", func(w http.ResponseWriter, r *http.Request) {
		query, parseErr := url.ParseQuery(r.URL.RawQuery)
		if parseErr != nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "invalid game event query"))
			return
		}
		for key := range query {
			if key != "after" && key != "limit" {
				WriteError(w, domain.E(domain.CodeValidationFailed, "unknown game event query parameter"))
				return
			}
		}
		after, err := consoleNumber(r, "after", 0, 0, 1<<62)
		if err != nil {
			WriteError(w, err)
			return
		}
		limit, err := consoleNumber(r, "limit", 100, 1, 200)
		if err != nil {
			WriteError(w, err)
			return
		}
		if s == nil {
			WriteError(w, domain.E(domain.CodeUnavailable, "game event history unavailable"))
			return
		}
		page, err := s.History(r.Context(), chi.URLParam(r, "instanceId"), after, int(limit))
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})
}
