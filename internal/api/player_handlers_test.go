package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/players"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPlayersHTTPRoleScopeAndInputBoundaries(t *testing.T) {
	d, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	p, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instances := instance.New(d, p, instanceTestGuard{})
	_, err = instances.Create(t.Context(), domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	q := jobs.New(d, nil)
	s := players.New(d, p, instances, nil, q, nil)
	if err = s.Register(); err != nil {
		t.Fatal(err)
	}
	if err = q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	router := chi.NewRouter()
	MountPlayers(router, s)
	request := func(method, path, body string, role domain.Role, token bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Username: "admin", Role: role}))
		}
		if token {
			r = r.WithContext(auth.WithTokenAuth(r.Context()))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/instances/alpha/players", "/instances/alpha/players/history", "/instances/alpha/players/lists/operators"} {
		if w := request("GET", path, "", "", false); w.Code != 401 {
			t.Fatal("anonymous", path, w.Code)
		}
	}
	for _, tc := range []struct {
		method, path, body string
		role               domain.Role
		token              bool
		status             int
	}{{"GET", "/instances/alpha/players", "", domain.RoleViewer, false, 200}, {"GET", "/instances/alpha/players/history?limit=501", "", domain.RoleViewer, false, 422}, {"GET", "/instances/alpha/players/history?after=1&after=2", "", domain.RoleViewer, false, 422}, {"GET", "/instances/alpha/players/lists/../../passwd", "", domain.RoleViewer, false, 404}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"kick","name":"Alex"}`, domain.RoleViewer, false, 403}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"op","name":"Alex"}`, domain.RoleOperator, false, 403}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"op","name":"Alex"}`, domain.RoleAdmin, true, 403}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"kick","name":"Alex\nstop"}`, domain.RoleOperator, false, 422}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"kick","name":"Alex","reason":"unsafe"}`, domain.RoleOperator, false, 422}, {"POST", "/instances/alpha/players/mutations", `{"mode":"online","action":"kick","name":"Alex"}`, domain.RoleOperator, false, 202}} {
		w := request(tc.method, tc.path, tc.body, tc.role, tc.token)
		if w.Code != tc.status {
			t.Fatalf("%s %s %s: %d %s", tc.method, tc.path, tc.body, w.Code, w.Body.String())
		}
	}
}
