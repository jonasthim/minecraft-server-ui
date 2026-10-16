package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/backup"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
)

func TestBackupHTTPRoleMatrixAndStrictInputs(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, _ := instance.NewPaths(t.TempDir())
	instances := instance.New(database, paths, instanceTestGuard{})
	if _, err = instances.Create(t.Context(), domain.User{ID: 1, Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at)VALUES(1,'operator','operator','2026-10-16','2026-10-16')`)
	q := jobs.New(database, nil)
	q.Register("backup_create", func(context.Context, *jobs.Execution) error { return nil })
	if err = q.StartPaused(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer q.Stop(context.Background())
	s := backup.New(backup.Config{DB: database, Paths: paths, Instances: instances, Jobs: q})
	r := chi.NewRouter()
	MountBackups(r, s)
	request := func(method, path, body string, role domain.Role) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			req = req.WithContext(auth.WithUser(req.Context(), &domain.User{ID: 1, Role: role}))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		want := 200
		if role == "" {
			want = 401
		}
		w := request("GET", "/instances/alpha/backups", "", role)
		if w.Code != want {
			t.Fatalf("%s list %d %s", role, w.Code, w.Body)
		}
		for _, tc := range []struct{ method, path, body string }{{"POST", "/instances/alpha/backups", `{"kind":"world"}`}, {"POST", "/instances/alpha/backups/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/restore", `{"confirmed":false}`}, {"GET", "/instances/alpha/backups/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/download", ""}} {
			w = request(tc.method, tc.path, tc.body, role)
			if role == "" && w.Code != 401 || role == domain.RoleViewer && w.Code != 403 {
				t.Fatalf("role gate %s %s %d", role, tc.path, w.Code)
			}
		}
	}
	for _, body := range []string{`{"kind":"world","trigger":"automatic"}`, `{"kind":"world","path":"/etc"}`, `{"kind":"other"}`} {
		w := request("POST", "/instances/alpha/backups", body, domain.RoleOperator)
		if w.Code != 422 {
			t.Fatalf("invalid input %s: %d %s", body, w.Code, w.Body)
		}
	}
	w := request("POST", "/instances/alpha/backups/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/restore", `{"confirmed":false}`, domain.RoleOperator)
	if w.Code != 422 {
		t.Fatal("confirmation not enforced", w.Code)
	}
}
