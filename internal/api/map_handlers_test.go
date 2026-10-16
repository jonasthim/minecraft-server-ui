package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/mapdata"
	"github.com/jonasthim/minecraft-server-ui/internal/mapview"
	"net/http/httptest"
	"testing"
)

type mapHTTP struct {
	calls   int
	request mapdata.Request
}

func (s *mapHTTP) Summary(context.Context, string) (mapdata.Summary, error) {
	s.calls++
	return mapdata.Summary{State: mapdata.Missing}, nil
}
func (s *mapHTTP) View(_ context.Context, _ string, r mapdata.Request) (mapview.View, error) {
	s.calls++
	s.request = r
	return mapview.View{Tiles: []mapdata.Tile{}}, nil
}
func TestMapRoleAndStrictQueries(t *testing.T) {
	s := &mapHTTP{}
	r := chi.NewRouter()
	MountMap(r, s)
	request := func(path string, role domain.Role) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &domain.User{ID: 1, Role: role}))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := request("/instances/alpha/map", ""); w.Code != 401 || s.calls != 0 {
		t.Fatal(w.Code, s.calls)
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		if w := request("/instances/alpha/map", role); w.Code != 200 {
			t.Fatal(role, w.Code)
		}
	}
	for _, query := range []string{"dimension=x&chunk_x=0", "dimension=x&chunk_x=0&chunk_z=0&max_y=", "dimension=x&chunk_x=0&chunk_z=0&path=secret", "dimension=x&dimension=y&chunk_x=0&chunk_z=0", "dimension=x&chunk_x=2147483648&chunk_z=0"} {
		before := s.calls
		if w := request("/instances/alpha/map/view?"+query, domain.RoleViewer); w.Code != 422 || s.calls != before {
			t.Fatal(query, w.Code, s.calls)
		}
	}
	w := request("/instances/alpha/map/view?dimension=example:custom&chunk_x=-2&chunk_z=3&max_y=-64", domain.RoleViewer)
	if w.Code != 200 || s.request.MaxY == nil || *s.request.MaxY != -64 || s.request.Dimension != "example:custom" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, s.request, w.Header())
	}
}

func TestMapReauthenticatesRevokedReadToken(t *testing.T) {
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	identity := auth.NewService(database, auth.Config{BaseURL: "http://example.com", InsecureCookies: true}, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/setup", nil)
	if _, e = identity.Setup(t.Context(), w, req, "admin", "very-strong-password", "", ""); e != nil {
		t.Fatal(e)
	}
	user, e := identity.Create(t.Context(), "viewer", "very-strong-password", "", "", domain.RoleViewer)
	if e != nil {
		t.Fatal(e)
	}
	token, secret, e := identity.CreateScopedToken(t.Context(), user, "map-view", 1, []string{"read"})
	if e != nil {
		t.Fatal(e)
	}
	s := &mapHTTP{}
	r := chi.NewRouter()
	r.Use(identity.Authenticate, identity.CSRF)
	MountMap(r, s)
	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/instances/alpha/map/view?dimension=minecraft:overworld&chunk_x=0&chunk_z=0", nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w = get(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e = identity.RevokeToken(t.Context(), user.ID, token.ID); e != nil {
		t.Fatal(e)
	}
	before := s.calls
	if w = get(); w.Code != 401 || s.calls != before {
		t.Fatal("revoked viewer retained access", w.Code, s.calls)
	}
}
