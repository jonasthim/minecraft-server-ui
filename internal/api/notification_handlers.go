package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/notify"
	"net/http"
	"strconv"
)

type NotificationService interface {
	List(context.Context) ([]notify.Channel, error)
	Save(context.Context, string, notify.ChannelInput, domain.User) (notify.Channel, error)
	Delete(context.Context, string, domain.User) error
	History(context.Context, int64, int) (notify.History, error)
}

func MountNotifications(r chi.Router, s NotificationService) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleAdmin))
		r.Get("/notifications/channels", func(w http.ResponseWriter, r *http.Request) {
			if len(r.URL.Query()) != 0 {
				WriteError(w, domain.E(domain.CodeValidationFailed, "Unknown notification parameter."))
				return
			}
			channels, e := s.List(r.Context())
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, 200, map[string]any{"channels": channels})
		})
		r.Put("/notifications/channels/{channelId}", func(w http.ResponseWriter, r *http.Request) {
			var in notify.ChannelInput
			if e := DecodeJSON(r, &in); e != nil {
				WriteError(w, e)
				return
			}
			channel, e := s.Save(r.Context(), chi.URLParam(r, "channelId"), in, *auth.UserFrom(r.Context()))
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, 200, channel)
		})
		r.Delete("/notifications/channels/{channelId}", func(w http.ResponseWriter, r *http.Request) {
			if e := s.Delete(r.Context(), chi.URLParam(r, "channelId"), *auth.UserFrom(r.Context())); e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, 204, nil)
		})
		r.Get("/notifications/history", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			for key, values := range q {
				if len(values) != 1 || (key != "before" && key != "limit") {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Unknown or repeated notification history parameter."))
					return
				}
			}
			before := int64(0)
			limit := 50
			var e error
			if value, ok := q["before"]; ok {
				before, e = strconv.ParseInt(value[0], 10, 64)
				if e != nil || before < 0 {
					WriteError(w, domain.E(domain.CodeValidationFailed, "Invalid history cursor."))
					return
				}
			}
			if value, ok := q["limit"]; ok {
				limit, e = strconv.Atoi(value[0])
				if e != nil || limit < 1 || limit > 100 {
					WriteError(w, domain.E(domain.CodeValidationFailed, "History limit must be 1–100."))
					return
				}
			}
			page, e := s.History(r.Context(), before, limit)
			if e != nil {
				WriteError(w, e)
				return
			}
			WriteJSON(w, 200, page)
		})
	})
}
