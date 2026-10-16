package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/metrics"
	"net/http"
	"time"
)

type MetricsService interface {
	Query(context.Context, metrics.Query) (metrics.Page, error)
}

// MountMetrics is composed under the shared Authenticate/CSRF middleware. Each
// finite query revalidates the current session/PAT role and read scope.
func MountMetrics(r chi.Router, s MetricsService) {
	r.With(auth.RequireRole(domain.RoleViewer)).Get("/instances/{instanceId}/metrics", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for key, values := range q {
			if len(values) != 1 || (key != "from" && key != "to" && key != "resolution") {
				WriteError(w, domain.E(domain.CodeValidationFailed, "Unknown or repeated metrics parameter."))
				return
			}
		}
		from, e := time.Parse(time.RFC3339Nano, q.Get("from"))
		if e != nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "Metrics from must be an RFC3339 timestamp."))
			return
		}
		to, e := time.Parse(time.RFC3339Nano, q.Get("to"))
		if e != nil {
			WriteError(w, domain.E(domain.CodeValidationFailed, "Metrics to must be an RFC3339 timestamp."))
			return
		}
		resolution := q.Get("resolution")
		if resolution != "raw" && resolution != "hourly" {
			WriteError(w, domain.E(domain.CodeValidationFailed, "Metrics resolution must be raw or hourly."))
			return
		}
		page, e := s.Query(r.Context(), metrics.Query{InstanceID: chi.URLParam(r, "instanceId"), From: from, To: to, Resolution: resolution})
		if e != nil {
			WriteError(w, e)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})
}
