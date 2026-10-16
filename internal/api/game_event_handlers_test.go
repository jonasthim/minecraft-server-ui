package api

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events/game"
)

type gameEventsHTTP struct{ calls int }

func (s *gameEventsHTTP) History(context.Context, string, int64, int) (game.Page, error) {
	s.calls++
	return game.Page{Events: []game.Event{}}, nil
}
func TestGameEventsRolesAndStrictBounds(t *testing.T) {
	source := &gameEventsHTTP{}
	router := chi.NewRouter()
	MountGameEvents(router, source)
	get := func(query string, user *domain.User, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/instances/alpha/game-events"+query, nil)
		if user != nil {
			r = r.WithContext(auth.WithUser(r.Context(), user))
		}
		if token {
			r = r.WithContext(auth.WithTokenAuth(r.Context()))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := get("", nil, false); w.Code != 401 || source.calls != 0 {
		t.Fatal(w.Code, source.calls)
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		if w := get("", &domain.User{ID: 1, Role: role}, false); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(role, w.Code, w.Body.String())
		}
	}
	for _, query := range []string{"?limit=%zz", "?limit=201", "?limit=0", "?after=-1", "?after=1&after=2", "?limit=", "?unknown=1", "?after=abc", "?after=9223372036854775808"} {
		before := source.calls
		if w := get(query, &domain.User{ID: 1, Role: domain.RoleViewer}, false); w.Code != 422 || source.calls != before {
			t.Fatal(query, w.Code, w.Body.String())
		}
	}
	if w := get("", &domain.User{ID: 1, Role: domain.RoleAdmin}, true); w.Code != 403 {
		t.Fatal("scope bypass", w.Code)
	}
	if w := get("", &domain.User{ID: 1, Role: domain.RoleViewer, Disabled: true}, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestGameEventsReauthenticatesReadPAT(t *testing.T) {
	d, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	identity := auth.NewService(d, auth.Config{BaseURL: "http://example.com", InsecureCookies: true}, nil)
	if _, err = identity.Setup(t.Context(), httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/setup", nil), "admin", "very-strong-password", "", ""); err != nil {
		t.Fatal(err)
	}
	viewer, err := identity.Create(t.Context(), "viewer", "very-strong-password", "", "", domain.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	token, secret, err := identity.CreateScopedToken(t.Context(), viewer, "events", 1, []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	source := &gameEventsHTTP{}
	router := chi.NewRouter()
	router.Use(identity.Authenticate, identity.CSRF)
	MountGameEvents(router, source)
	get := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/instances/alpha/game-events", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := get(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err = identity.RevokeToken(t.Context(), viewer.ID, token.ID); err != nil {
		t.Fatal(err)
	}
	before := source.calls
	if w := get(); w.Code != 401 || source.calls != before {
		t.Fatal("revoked PAT", w.Code, source.calls)
	}
}
