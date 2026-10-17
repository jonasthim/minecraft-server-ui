package api

import (
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"net/http"
	"strconv"
	"time"
)

// MountJobs expects the shared authenticated /api/v1 router, including CSRF
// protection. Every endpoint also installs its own role/token scope gate.
// Feature endpoints enqueue only their own authorized operation; there is no
// generic client-supplied job kind or private-payload enqueue endpoint.
func MountJobs(r chi.Router, s *jobs.Service) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(domain.RoleViewer))
		r.Get("/jobs", func(w http.ResponseWriter, r *http.Request) {
			v, err := s.List(r.Context(), r.URL.Query().Get("instance_id"))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"jobs": v})
		})
		r.Get("/jobs/{jobId}", func(w http.ResponseWriter, r *http.Request) {
			v, err := s.Get(r.Context(), chi.URLParam(r, "jobId"))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"job": v})
		})
		r.Get("/jobs/{jobId}/logs", func(w http.ResponseWriter, r *http.Request) {
			after, err := jobCursor(r, "after", 0, 0, 1<<62)
			if err != nil {
				WriteError(w, err)
				return
			}
			limit, err := jobCursor(r, "limit", 100, 1, 500)
			if err != nil {
				WriteError(w, err)
				return
			}
			v, err := s.Logs(r.Context(), chi.URLParam(r, "jobId"), after, int(limit))
			if err != nil {
				WriteError(w, err)
				return
			}
			WriteJSON(w, 200, map[string]any{"logs": v})
		})
		r.Get("/jobs/{jobId}/logs/download", func(w http.ResponseWriter, r *http.Request) { downloadJobLogs(w, r, s) })
		r.Get("/jobs/{jobId}/events", func(w http.ResponseWriter, r *http.Request) { streamJob(w, r, s) })
	})
	r.With(auth.RequireRole(domain.RoleOperator)).Post("/jobs/{jobId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "jobId")
		if err := s.Cancel(r.Context(), id, *auth.UserFrom(r.Context())); err != nil {
			WriteError(w, err)
			return
		}
		v, err := s.Get(r.Context(), id)
		if err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, 200, map[string]any{"job": v})
	})
}
func jobCursor(r *http.Request, key string, fallback, min, max int64) (int64, error) {
	value, ok := r.URL.Query()[key]
	if !ok {
		return fallback, nil
	}
	if len(value) != 1 {
		return 0, domain.E(domain.CodeValidationFailed, "invalid log cursor or limit")
	}
	n, err := strconv.ParseInt(value[0], 10, 64)
	if err != nil || n < min || n > max {
		return 0, domain.E(domain.CodeValidationFailed, "invalid log cursor or limit")
	}
	return n, nil
}

type jobStreamTiming struct {
	now                 func() time.Time
	lifetime, heartbeat time.Duration
}

func streamJob(w http.ResponseWriter, r *http.Request, s *jobs.Service) {
	streamJobWithTiming(w, r, s, jobStreamTiming{now: time.Now, lifetime: 30 * time.Second, heartbeat: 15 * time.Second})
}
func streamJobWithTiming(w http.ResponseWriter, r *http.Request, s *jobs.Service, timing jobStreamTiming) {
	// Establish the authorization boundary before lookup or any snapshot I/O.
	// A slow first flush must not restart the connection's permission lifetime.
	expires := timing.now().Add(timing.lifetime)
	lifetime := time.NewTimer(expires.Sub(timing.now()))
	defer lifetime.Stop()
	id := chi.URLParam(r, "jobId")
	sub, err := s.Subscribe(id)
	if err != nil {
		WriteError(w, err)
		return
	}
	defer sub.Close()
	j, err := s.Get(r.Context(), id)
	if err != nil {
		WriteError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	prepareWrite := func() bool {
		now := timing.now()
		if !now.Before(expires) || r.Context().Err() != nil {
			return false
		}
		deadline := now.Add(5 * time.Second)
		if expires.Before(deadline) {
			deadline = expires
		}
		_ = controller.SetWriteDeadline(deadline)
		return true
	}
	write := func(event events.Event) bool {
		data, err := json.Marshal(event)
		if err != nil {
			return false
		}
		if !prepareWrite() {
			return false
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return
	}
	if !write(events.Event{Type: "progress", JobID: id, Job: raw}) {
		return
	}
	heartbeat := time.NewTicker(timing.heartbeat)
	defer heartbeat.Stop()
	// Each connection reauthorizes within 30s, bounding revocation/role staleness
	// without retaining or leaking the bearer or session credential into the hub.
	for {
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			return
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			if !write(event) {
				return
			}
		case <-heartbeat.C:
			if !prepareWrite() {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
		}
	}
}
func downloadJobLogs(w http.ResponseWriter, r *http.Request, s *jobs.Service) {
	id := chi.URLParam(r, "jobId")
	if _, err := s.Get(r.Context(), id); err != nil {
		WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="job-%s.log"`, id))
	controller := http.NewResponseController(w)
	var cursor int64
	size := 0
	for {
		logs, err := s.Logs(r.Context(), id, cursor, 500)
		if err != nil {
			return
		}
		if len(logs) == 0 {
			return
		}
		for _, line := range logs {
			value := line.CreatedAt.Format(time.RFC3339Nano) + " " + line.Message + "\n"
			if size+len(value) > 5<<20 {
				_, _ = fmt.Fprint(w, "[Download truncated at 5 MiB; use paginated logs for the remaining output.]\n")
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprint(w, value); err != nil {
				return
			}
			size += len(value)
			cursor = line.ID
		}
	}
}
