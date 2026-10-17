package api

import (
	"context"
	"encoding/json"
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

func TestBackupRetentionHTTPAuthorizationAndPolicyContract(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	instances := instance.New(database, paths, instanceTestGuard{})
	if _, err := instances.Create(t.Context(), domain.User{ID: 1, Role: domain.RoleAdmin}, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.16.5", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','2026-10-17','2026-10-17')`); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	MountBackups(r, backup.New(backup.Config{DB: database, Paths: paths, Instances: instances}))
	request := func(method, id, body string, role domain.Role) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/instances/"+id+"/backups/retention", strings.NewReader(body))
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
		w := request("GET", "alpha", "", role)
		if w.Code != want {
			t.Fatalf("GET role %q: %d %s", role, w.Code, w.Body)
		}
		if w.Code == 200 {
			var got backup.RetentionPolicy
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != (backup.RetentionPolicy{KeepLast: 10, KeepDays: 30}) {
				t.Fatalf("default response: %+v", got)
			}
		}
		want = 403
		if role == "" {
			want = 401
		} else if role == domain.RoleAdmin {
			want = 200
		}
		w = request("PUT", "alpha", `{"keep_last":10,"keep_days":30,"expected_revision":0}`, role)
		if w.Code != want {
			t.Fatalf("PUT role %q: %d %s", role, w.Code, w.Body)
		}
	}
	for _, body := range []string{
		`{}`, `null`, `{"keep_last":0,"keep_days":0}`, `{"keep_last":null,"keep_days":0,"expected_revision":0}`,
		`{"keep_last":0,"keep_days":0,"expected_revision":null}`, `{"keep_last":1.5,"keep_days":0,"expected_revision":0}`,
		`{"keep_last":10001,"keep_days":0,"expected_revision":0}`, `{"keep_last":0,"keep_days":36501,"expected_revision":0}`,
		`{"keep_last":0,"keep_days":0,"expected_revision":-1}`, `{"keep_last":0,"keep_days":0,"expected_revision":0,"trigger":"manual"}`,
	} {
		w := request("PUT", "alpha", body, domain.RoleAdmin)
		if w.Code != 422 {
			t.Fatalf("invalid body %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, tc := range []struct {
		id   string
		code int
	}{{"missing", 404}, {"Alpha", 422}} {
		if w := request("GET", tc.id, "", domain.RoleViewer); w.Code != tc.code {
			t.Fatalf("GET %s: %d %s", tc.id, w.Code, w.Body)
		}
	}
	w := request("PUT", "alpha", `{"keep_last":0,"keep_days":0,"expected_revision":0}`, domain.RoleAdmin)
	var saved backup.RetentionPolicy
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved != (backup.RetentionPolicy{Revision: 1}) {
		t.Fatalf("unlimited response: %+v", saved)
	}
	if w = request("PUT", "alpha", `{"keep_last":5,"keep_days":7,"expected_revision":0}`, domain.RoleAdmin); w.Code != 409 {
		t.Fatalf("stale save: %d %s", w.Code, w.Body)
	}
	if w = request("PUT", "alpha", `{"keep_last":0,"keep_days":0,"expected_revision":0}`, domain.RoleAdmin); w.Code != 200 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	var changes int
	if err := database.QueryRow(`SELECT count(*) FROM audit_log WHERE action='backups.retention_policy'`).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("policy mutation audits: %d", changes)
	}
	if _, err := database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'remaining-admin','admin','2026-10-17','2026-10-17')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE users SET role='viewer' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if w = request("PUT", "alpha", `{"keep_last":5,"keep_days":7,"expected_revision":1}`, domain.RoleAdmin); w.Code != 403 {
		t.Fatalf("revoked admin: %d %s", w.Code, w.Body)
	}
}

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
