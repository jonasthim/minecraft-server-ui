package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type instanceTestGuard struct{}

func (instanceTestGuard) WithStopped(ctx context.Context, id string, fn func() error) error {
	return fn()
}
func TestInstancesRoleMatrixAndEmptyDBCRUD(t *testing.T) {
	ctx := context.Background()
	database, e := db.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer database.Close()
	paths, e := instance.NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s := instance.New(database, paths, instanceTestGuard{})
	h := chi.NewRouter()
	MountInstances(h, s)
	request := func(method, path, body string, user *domain.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != nil {
			r = r.WithContext(auth.WithUser(r.Context(), user))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	draft := instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9", JavaMajor: 21, MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}
	raw, _ := json.Marshal(draft)
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		path := "/instances"
		if method == "PUT" || method == "DELETE" {
			path += "/alpha"
		}
		w := request(method, path, string(raw), nil)
		if w.Code != 401 {
			t.Fatalf("anonymous %s %d", method, w.Code)
		}
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator} {
		user := &domain.User{ID: 2, Role: role}
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			path := "/instances"
			if method != "POST" {
				path += "/alpha"
			}
			w := request(method, path, string(raw), user)
			if w.Code != 403 {
				t.Fatalf("%s %s: %d", role, method, w.Code)
			}
		}
	}
	admin := &domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
	w := request("GET", "/instances", "", admin)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"instances":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("POST", "/instances", string(raw), admin)
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		w = request("GET", "/instances/alpha", "", &domain.User{ID: 1, Role: role})
		if w.Code != 200 {
			t.Fatal(role, w.Code, w.Body.String())
		}
	}
	draft.Instance.Name = "Updated"
	raw, _ = json.Marshal(draft)
	w = request("PUT", "/instances/alpha", string(raw), admin)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("DELETE", "/instances/alpha", `{"confirmation":"wrong"}`, admin)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("DELETE", "/instances/alpha", `{"confirmation":"alpha"}`, admin)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var count int
	if e = database.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('instance.create','instance.update','instance.delete')`).Scan(&count); e != nil || count != 3 {
		t.Fatal(count, e)
	}
}

func TestInstanceSettingsCapabilitiesRoleAndVersionAvailability(t *testing.T) {
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := instance.New(database, paths, instanceTestGuard{})
	r := chi.NewRouter()
	MountInstances(r, s)
	for _, test := range []struct {
		role    domain.Role
		version string
		want    int
	}{{"", "1.12.2", 401}, {domain.RoleViewer, "1.12.2", 200}, {domain.RoleOperator, "1.18", 200}, {domain.RoleAdmin, "1.21.9", 200}, {domain.RoleViewer, "snapshot", 422}} {
		var user *domain.User
		if test.role != "" {
			user = &domain.User{ID: 1, Role: test.role}
		}
		w := httptest.NewRecorder()
		req := userRequest("GET", "/instance-settings/capabilities?flavor=vanilla&minecraft_version="+test.version, user)
		r.ServeHTTP(w, req)
		if w.Code != test.want {
			t.Fatal(test, w.Code, w.Body.String())
		}
		if test.want == 200 && (!strings.Contains(w.Body.String(), `"settings"`) || strings.Contains(w.Body.String(), `"status":"applied"`)) {
			t.Fatal(w.Body.String())
		}
	}
}
