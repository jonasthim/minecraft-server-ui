package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/modpack"
	"net/http/httptest"
	"strings"
	"testing"
)

type noPackCatalog struct{}

func (noPackCatalog) Project(context.Context, catalog.Provider, string) (catalog.Project, error) {
	return catalog.Project{}, domain.NotFound("pack")
}
func (noPackCatalog) Version(context.Context, catalog.Provider, string, string) (catalog.Version, error) {
	return catalog.Version{}, domain.NotFound("pack")
}
func (noPackCatalog) Download(context.Context, catalog.Selection) (catalog.VerifiedArtifact, error) {
	return catalog.VerifiedArtifact{}, domain.NotFound("pack")
}
func TestModpackRoleAndPATScopeAdmission(t *testing.T) {
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	users := []domain.User{{ID: 1, Username: "admin", Role: domain.RoleAdmin}, {ID: 2, Username: "operator", Role: domain.RoleOperator}, {ID: 3, Username: "viewer", Role: domain.RoleViewer}}
	for _, u := range users {
		if _, e = database.Exec("INSERT INTO users(id,username,role,created_at,updated_at) VALUES(?,?,?,'now','now')", u.ID, u.Username, u.Role); e != nil {
			t.Fatal(e)
		}
	}
	paths, _ := instance.NewPaths(t.TempDir())
	instances := instance.New(database, paths, instanceTestGuard{})
	_, e = instances.Create(t.Context(), users[0], instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorFabric, MinecraftVersion: "1.20.1", LoaderVersion: "0.16.0", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if e != nil {
		t.Fatal(e)
	}
	queue := jobs.New(database, nil)
	cs, e := content.New(content.Config{DB: database, Paths: paths, Instances: instances, Queue: queue})
	if e != nil {
		t.Fatal(e)
	}
	service, e := modpack.New(modpack.Config{DB: database, Paths: paths, Instances: instances, Queue: queue, Content: cs, Catalog: noPackCatalog{}})
	if e != nil {
		t.Fatal(e)
	}
	security := auth.NewService(database, auth.Config{}, nil)
	tokens := map[string]string{}
	for _, u := range users {
		scopes := []string{"read"}
		if u.Role == domain.RoleOperator {
			scopes = []string{"operate"}
		}
		if u.Role == domain.RoleAdmin {
			scopes = []string{"admin"}
		}
		_, token, e := security.CreateScopedToken(t.Context(), &u, u.Username, 1, scopes)
		if e != nil {
			t.Fatal(e)
		}
		tokens[string(u.Role)] = token
	}
	_, token, e := security.CreateScopedToken(t.Context(), &users[0], "limited", 1, []string{"operate"})
	if e != nil {
		t.Fatal(e)
	}
	tokens["limited"] = token
	r := chi.NewRouter()
	r.Use(security.Authenticate, security.CSRF)
	MountModpacks(r, service)
	request := func(role, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			req.Header.Set("Authorization", "Bearer "+tokens[role])
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	base := "/instances/alpha/modpack"
	for _, role := range []string{"viewer", "operator", "admin"} {
		if code := request(role, "GET", base, ""); code != 200 {
			t.Fatal(role, code)
		}
	}
	for _, role := range []string{"", "viewer"} {
		want := 403 // CSRF rejects anonymous mutations before role admission.
		for _, path := range []string{base + "/previews", base + "/previews/01234567890123456789012345678901/apply"} {
			if code := request(role, "POST", path, `{}`); code != want {
				t.Fatal(role, path, code)
			}
		}
	}
	for _, role := range []string{"viewer", "operator", "limited"} {
		for _, route := range []string{base + "/previews/01234567890123456789012345678901/members/resume?member=config/a.cfg", base + "/archives?provider=modrinth&project_id=pack&version_id=v&file_id=f"} {
			if code := request(role, "POST", route, "bytes"); code != 403 {
				t.Fatal("manual resume scope", role, code)
			}
		}
	}
	if code := request("operator", "POST", base+"/previews", `{"unknown_field":true}`); code != 422 {
		t.Fatal("untyped input accepted", code)
	}
}
