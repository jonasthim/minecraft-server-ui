package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/notify"
	"net/http/httptest"
	"strings"
	"testing"
)

type notificationsHTTP struct{ calls int }

func (f *notificationsHTTP) List(context.Context) ([]notify.Channel, error) {
	f.calls++
	return []notify.Channel{}, nil
}
func (f *notificationsHTTP) Save(context.Context, string, notify.ChannelInput, domain.User) (notify.Channel, error) {
	f.calls++
	return notify.Channel{}, nil
}
func (f *notificationsHTTP) Delete(context.Context, string, domain.User) error { f.calls++; return nil }
func (f *notificationsHTTP) History(context.Context, int64, int) (notify.History, error) {
	f.calls++
	return notify.History{Deliveries: []notify.Delivery{}}, nil
}
func TestNotificationAdminRoleStrictInputAndScope(t *testing.T) {
	f := &notificationsHTTP{}
	router := chi.NewRouter()
	MountNotifications(router, f)
	for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		for _, test := range []struct{ method, path, body string }{{"GET", "/notifications/channels", ""}, {"GET", "/notifications/history", ""}, {"PUT", "/notifications/channels/ops", `{"label":"Ops","provider":"webhook","enabled":false,"events":[],"clear":false,"allow_private":false}`}, {"DELETE", "/notifications/channels/ops", ""}} {
			r := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if role != "" {
				r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: role}))
			}
			w := httptest.NewRecorder()
			calls := f.calls
			router.ServeHTTP(w, r)
			want := 403
			if role == "" {
				want = 401
			}
			if role == domain.RoleAdmin {
				want = 200
				if test.method == "DELETE" {
					want = 204
				}
			}
			if w.Code != want || role != domain.RoleAdmin && calls != f.calls {
				t.Fatal(role, test.path, w.Code, f.calls)
			}
		}
	}
	for _, path := range []string{"/notifications/history?limit=101", "/notifications/history?before=-1", "/notifications/history?limit=1&limit=2", "/notifications/channels?endpoint=secret"} {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: domain.RoleAdmin}))
		w := httptest.NewRecorder()
		calls := f.calls
		router.ServeHTTP(w, r)
		if w.Code != 422 || f.calls != calls {
			t.Fatal(path, w.Code, f.calls)
		}
	}
}
func TestObservabilityReauthenticatesScopedAndRevokedPAT(t *testing.T) {
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	identity := auth.NewService(database, auth.Config{BaseURL: "http://example.com", InsecureCookies: true}, nil)
	_, e = identity.Setup(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/setup", nil), "admin", "very-strong-password", "", "")
	if e != nil {
		t.Fatal(e)
	}
	user, e := identity.Create(t.Context(), "reviewer", "very-strong-password", "", "", domain.RoleAdmin)
	if e != nil {
		t.Fatal(e)
	}
	f := &notificationsHTTP{}
	m := &metricsHTTP{}
	router := chi.NewRouter()
	router.Use(identity.Authenticate, identity.CSRF)
	MountNotifications(router, f)
	MountMetrics(router, m)
	get := func(path, secret string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	_, read, e := identity.CreateScopedToken(t.Context(), user, "read", 1, []string{"read"})
	if e != nil {
		t.Fatal(e)
	}
	metricPath := "/instances/alpha/metrics?from=2026-10-16T12:00:00Z&to=2026-10-16T13:00:00Z&resolution=raw"
	if w := get(metricPath, read); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := get("/notifications/channels", read); w.Code != 403 || f.calls != 0 {
		t.Fatal("read PAT reached private channels", w.Code)
	}
	token, secret, e := identity.CreateScopedToken(t.Context(), user, "admin", 1, []string{"admin"})
	if e != nil {
		t.Fatal(e)
	}
	if w := get("/notifications/history", secret); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if e = identity.RevokeToken(t.Context(), user.ID, token.ID); e != nil {
		t.Fatal(e)
	}
	before := f.calls
	if w := get("/notifications/history", secret); w.Code != 401 || f.calls != before {
		t.Fatal("revoked PAT retained history access", w.Code)
	}
	if _, e = database.Exec(`UPDATE users SET disabled=1 WHERE id=?`, user.ID); e != nil {
		t.Fatal(e)
	}
	calls := m.calls
	if w := get(metricPath, read); w.Code != 401 || m.calls != calls {
		t.Fatal("disabled account retained metrics access", w.Code)
	}
}
