package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/scheduler"
	"net/http"
)

type ScheduleService interface {
	List(context.Context, string) ([]scheduler.Record, error)
	Save(context.Context, string, string, scheduler.Input, domain.User) (scheduler.Record, error)
	Delete(context.Context, string, string, domain.User) error
	History(context.Context, string, string) ([]scheduler.Run, error)
	Recovery(context.Context, string) (scheduler.RecoveryStatus, error)
	ConfigureRecovery(context.Context, string, bool, domain.User) (scheduler.RecoveryStatus, error)
	DefaultTimezone() string
	AvailableActions() []domain.ScheduleAction
}

// MountSchedules belongs inside the authenticated and CSRF-protected group.
// Definitions preserve owner authority; jobs remain visible through jobs APIs.
func MountSchedules(router chi.Router, s ScheduleService) {
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/schedules", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			WriteError(w, domain.E(domain.CodeValidationFailed, "unknown schedule query parameter"))
			return
		}
		records, e := s.List(r.Context(), chi.URLParam(r, "instanceId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"schedules": records, "default_timezone": s.DefaultTimezone(), "time_policy": scheduler.TimePolicy, "available_actions": s.AvailableActions()})
	})
	save := func(status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var in scheduler.Input
			if e := DecodeJSON(r, &in); e != nil {
				WriteError(w, e)
				return
			}
			record, e := s.Save(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "scheduleId"), in, *auth.UserFrom(r.Context()))
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, status, record)
		}
	}
	router.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/schedules", save(http.StatusCreated))
	router.With(auth.RequireRole(domain.RoleOperator)).Put("/instances/{instanceId}/schedules/{scheduleId}", save(http.StatusOK))
	router.With(auth.RequireRole(domain.RoleOperator)).Delete("/instances/{instanceId}/schedules/{scheduleId}", func(w http.ResponseWriter, r *http.Request) {
		if e := s.Delete(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "scheduleId"), *auth.UserFrom(r.Context())); e != nil {
			WriteError(w, e)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/schedules/{scheduleId}/runs", func(w http.ResponseWriter, r *http.Request) {
		history, e := s.History(r.Context(), chi.URLParam(r, "instanceId"), chi.URLParam(r, "scheduleId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, map[string]any{"runs": history})
	})
	router.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/recovery", func(w http.ResponseWriter, r *http.Request) {
		result, e := s.Recovery(r.Context(), chi.URLParam(r, "instanceId"))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, result)
	})
	router.With(auth.RequireRole(domain.RoleAdmin)).Put("/instances/{instanceId}/recovery", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if e := DecodeJSON(r, &in); e != nil {
			WriteError(w, e)
			return
		}
		if in.Enabled == nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "enabled is required"))
			return
		}
		result, e := s.ConfigureRecovery(r.Context(), chi.URLParam(r, "instanceId"), *in.Enabled, *auth.UserFrom(r.Context()))
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, 200, result)
	})
}
