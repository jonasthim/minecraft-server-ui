package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

// MountConsole requires the shared authenticated/CSRF-protected API group. Each
// route separately enforces its role and token scope before reads or allocation.
func MountConsole(r chi.Router, s *console.Service) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleViewer))
		r.Get("/instances/{instanceId}/console/logs", func(w http.ResponseWriter, r *http.Request) {
			after, limit, err := consoleCursor(r)
			if err != nil {
				WriteError(w, err)
				return
			}
			page, err := s.History(r.Context(), chi.URLParam(r, "instanceId"), after, limit)
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, page)
		})
		r.Get("/instances/{instanceId}/console/status", func(w http.ResponseWriter, r *http.Request) {
			state, err := s.Status(r.Context(), chi.URLParam(r, "instanceId"))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"state": state})
		})
		r.Get("/instances/{instanceId}/console/events", func(w http.ResponseWriter, r *http.Request) { streamConsole(w, r, s) })
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/instances/{instanceId}/console/commands", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Command string `json:"command"`
		}
		if err := DecodeJSON(r, &input); err != nil {
			WriteError(w, err)
			return
		}
		execute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			job, err := s.EnqueueCommand(r.Context(), chi.URLParam(r, "instanceId"), input.Command, *auth.UserFrom(r.Context()))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, http.StatusAccepted, map[string]any{"job": job})
		})
		// Administrator identity does not imply an administrator-scoped token.
		if input.Command == "seed" {
			auth.RequireRole(domain.RoleAdmin)(execute).ServeHTTP(w, r)
			return
		}
		execute.ServeHTTP(w, r)
	})
}
func consoleCursor(r *http.Request) (int64, int, error) {
	after, err := consoleNumber(r, "after", 0, 0, 1<<62)
	if err != nil {
		return 0, 0, err
	}
	limit, err := consoleNumber(r, "limit", 100, 1, 500)
	if err != nil {
		return 0, 0, err
	}
	return after, int(limit), nil
}
func consoleNumber(r *http.Request, key string, fallback, min, max int64) (int64, error) {
	v, ok := r.URL.Query()[key]
	if !ok {
		return fallback, nil
	}
	if len(v) != 1 {
		return 0, domain.E(domain.CodeValidationFailed, "invalid console cursor or limit")
	}
	n, err := strconv.ParseInt(v[0], 10, 64)
	if err != nil || n < min || n > max {
		return 0, domain.E(domain.CodeValidationFailed, "invalid console cursor or limit")
	}
	return n, nil
}

type consoleStreamTiming struct {
	now            func() time.Time
	lifetime, poll time.Duration
}
type consoleEvent struct {
	Page  console.Page  `json:"page"`
	State console.State `json:"state"`
}

func streamConsole(w http.ResponseWriter, r *http.Request, s *console.Service) {
	streamConsoleWithTiming(w, r, s, consoleStreamTiming{now: time.Now, lifetime: 30 * time.Second, poll: 500 * time.Millisecond})
}
func streamConsoleWithTiming(w http.ResponseWriter, r *http.Request, s *console.Service, timing consoleStreamTiming) {
	// Absolute authorization lifetime begins before snapshot I/O and allocation.
	expires := timing.now().Add(timing.lifetime)
	ctx, cancel := context.WithTimeout(r.Context(), timing.lifetime)
	defer cancel()
	r = r.WithContext(ctx)
	after, limit, err := consoleCursor(r)
	if err != nil {
		WriteError(w, err)
		return
	}
	if value := r.Header.Get("Last-Event-ID"); value != "" {
		n, e := strconv.ParseInt(value, 10, 64)
		if e != nil || n < 0 || n > 1<<62 {
			WriteError(w, domain.E(domain.CodeValidationFailed, "invalid console reconnect cursor"))
			return
		}
		after = max(after, n)
	}
	id := chi.URLParam(r, "instanceId")
	release, err := s.AcquireStream(ctx, id)
	if err != nil {
		WriteError(w, err)
		return
	}
	defer release()
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	write := func() bool {
		page, err := s.History(ctx, id, after, limit)
		if err != nil {
			return false
		}
		state, err := s.Status(ctx, id)
		if err != nil {
			state = console.State{Status: "unknown", Reason: "Native readiness observation is unavailable.", LogsReason: "Live log ingestion could not be verified; retained history may be incomplete.", GameplayEffect: "not_verified", RuleScope: "native_server_rules", ObservedAt: timing.now()}
		}
		data, err := json.Marshal(consoleEvent{page, state})
		if err != nil {
			return false
		}
		now := timing.now()
		if !now.Before(expires) || ctx.Err() != nil {
			return false
		}
		deadline := now.Add(5 * time.Second)
		if expires.Before(deadline) {
			deadline = expires
		}
		_ = controller.SetWriteDeadline(deadline)
		if _, err = fmt.Fprintf(w, "id: %d\nevent: console\ndata: %s\n\n", page.NextCursor, data); err != nil {
			return false
		}
		if controller.Flush() != nil {
			return false
		}
		after = page.NextCursor
		return true
	}
	if !write() {
		return
	}
	poll := time.NewTicker(timing.poll)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if !write() {
				return
			}
		}
	}
}
