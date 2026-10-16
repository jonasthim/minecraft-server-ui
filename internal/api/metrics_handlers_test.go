package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/metrics"
	"net/http/httptest"
	"testing"
)

type metricsHTTP struct {
	calls int
	query metrics.Query
}

func (f *metricsHTTP) Query(_ context.Context, q metrics.Query) (metrics.Page, error) {
	f.calls++
	f.query = q
	return metrics.Page{InstanceID: q.InstanceID, Points: []metrics.Point{}}, nil
}
func TestMetricsRolesAndStrictQueries(t *testing.T) {
	f := &metricsHTTP{}
	router := chi.NewRouter()
	MountMetrics(router, f)
	get := func(query string, user *domain.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/instances/alpha/metrics?"+query, nil)
		if user != nil {
			r = r.WithContext(auth.WithUser(r.Context(), user))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	query := "from=2026-10-16T12:00:00Z&to=2026-10-16T13:00:00Z&resolution=raw"
	if w := get(query, nil); w.Code != 401 || f.calls != 0 {
		t.Fatal(w.Code, f.calls)
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		if w := get(query, &domain.User{ID: 1, Role: role}); w.Code != 200 {
			t.Fatal(role, w.Code)
		}
	}
	if w := get(query, &domain.User{ID: 1, Role: domain.RoleViewer, Disabled: true}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	for _, invalid := range []string{"", query + "&pid=1", query + "&from=other", "from=bad&to=bad&resolution=raw", "from=2026-10-16T12:00:00Z&to=2026-10-16T13:00:00Z&resolution=other"} {
		calls := f.calls
		if w := get(invalid, &domain.User{ID: 1, Role: domain.RoleViewer}); w.Code != 422 || f.calls != calls {
			t.Fatal(invalid, w.Code, f.calls)
		}
	}
}
