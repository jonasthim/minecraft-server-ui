package api

import (
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/players"
	"net/http"
)

// MountPlayers requires the shared authenticated and CSRF-protected API group.
func MountPlayers(r chi.Router, s *players.Service) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleViewer))
		r.Get("/instances/{instanceId}/players", func(w http.ResponseWriter, r *http.Request) {
			snapshot, err := s.Snapshot(r.Context(), chi.URLParam(r, "instanceId"))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"snapshot": snapshot})
		})
		r.Get("/instances/{instanceId}/players/history", func(w http.ResponseWriter, r *http.Request) {
			after, limit, err := consoleCursor(r)
			if err != nil {
				WriteError(w, err)
				return
			}
			eventAfter, err := consoleNumber(r, "event_after", 0, 0, 1<<62)
			if err != nil {
				WriteError(w, err)
				return
			}
			history, err := s.HistoryWithEvents(r.Context(), chi.URLParam(r, "instanceId"), after, eventAfter, limit)
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, history)
		})
		r.Get("/instances/{instanceId}/players/lists/{kind}", func(w http.ResponseWriter, r *http.Request) {
			list, err := s.Lists(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "kind"))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"list": list})
		})
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/players/mutations", func(w http.ResponseWriter, r *http.Request) {
		var input players.Mutation
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		execute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			job, err := s.Enqueue(r.Context(), chi.URLParam(r, "instanceId"), input, *auth.UserFrom(r.Context()))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
		})
		if input.Action == "op" || input.Action == "deop" {
			auth.RequireRole(domain.RoleAdmin)(execute).ServeHTTP(w, r)
			return
		}
		execute.ServeHTTP(w, r)
	})
}
