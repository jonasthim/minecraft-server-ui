package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/lifecycle"
)

func TestLifecycleHTTPRoleAndPrivateOperationBoundaries(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	actor, err := db.NewUsers(database).Create(t.Context(), db.NewUser{Username: "admin", Role: domain.RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instances := instance.New(database, paths, instanceTestGuard{})
	if _, err = instances.Create(t.Context(), *actor, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.20.1", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	queue := jobs.New(database, nil)
	service := lifecycle.New(lifecycle.Config{DB: database, Instances: instances, Jobs: queue})
	if err = service.RegisterJobs(); err != nil {
		t.Fatal(err)
	}
	if err = queue.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queue.Stop(context.Background())
	router := chi.NewRouter()
	MountLifecycle(router, service)
	request := func(method, path, body string, role domain.Role) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			user := *actor
			user.Role = role
			req = req.WithContext(auth.WithUser(req.Context(), &user))
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	for _, tc := range []struct {
		method, path, body string
		role               domain.Role
		want               int
	}{
		{"GET", "/instances/alpha/lifecycle", "", "", 401},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"start"}`, "", 401},
		{"GET", "/instances/alpha/lifecycle", "", domain.RoleViewer, 200},
		{"POST", "/instances/alpha/lifecycle/preview", `{"flavor":"vanilla","minecraft_version":"1.20.1"}`, domain.RoleViewer, 403},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"start"}`, domain.RoleViewer, 403},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"start"}`, domain.RoleOperator, 202},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"autostart","enabled":true}`, domain.RoleOperator, 403},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"uninstall","confirmation":"alpha"}`, domain.RoleOperator, 403},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"stop","emergency":true,"confirmation":"alpha"}`, domain.RoleOperator, 403},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"stop","countdown_seconds":-1}`, domain.RoleOperator, 422},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"start","executable":"/bin/true"}`, domain.RoleAdmin, 422},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"lifecycle_verify"}`, domain.RoleAdmin, 422},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"uninstall"}`, domain.RoleAdmin, 409},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"stop","emergency":true}`, domain.RoleAdmin, 409},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"autostart","enabled":false}`, domain.RoleAdmin, 202},
		{"POST", "/instances/alpha/lifecycle", `{"operation":"uninstall","confirmation":"alpha"}`, domain.RoleAdmin, 202},
	} {
		response := request(tc.method, tc.path, tc.body, tc.role)
		if response.Code != tc.want {
			t.Fatalf("%s %s %s role=%s: got%d want%d %s", tc.method, tc.path, tc.body, tc.role, response.Code, tc.want, response.Body)
		}
		if strings.Contains(response.Body.String(), "launch.json") || strings.Contains(response.Body.String(), "java_runtime_id") {
			t.Fatal("private launch data exposed", response.Body.String())
		}
	}
	var audit int
	if err = database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action LIKE 'lifecycle.%.request' AND user_id=? AND details_json='{}'`, actor.ID).Scan(&audit); err != nil || audit != 3 {
		t.Fatalf("audit=%d err=%v", audit, err)
	}
}
