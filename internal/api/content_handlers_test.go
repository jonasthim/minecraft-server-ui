package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/content"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContentRoleAndScopedTokenMatrix(t *testing.T) {
	ctx := context.Background()
	database, e := db.OpenMemory(ctx)
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
	paths, e := instance.NewPaths(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	instances := instance.New(database, paths, instanceTestGuard{})
	_, e = instances.Create(ctx, users[0], instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorFabric, MinecraftVersion: "1.21.1", LoaderVersion: "0.16.5", JavaMajor: 21, MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if e != nil {
		t.Fatal(e)
	}
	dirs, _ := paths.For("alpha")
	os.MkdirAll(filepath.Join(dirs.Server, "config"), 0700)
	os.WriteFile(filepath.Join(dirs.Server, "config/secret.toml"), []byte("password=config-secret-canary\n"), 0600)
	if _, e = database.Exec("UPDATE instance_settings SET provisioning='installed' WHERE instance_id='alpha'"); e != nil {
		t.Fatal(e)
	}
	queue := jobs.New(database, nil)
	service, e := content.New(content.Config{DB: database, Paths: paths, Instances: instances, Queue: queue})
	if e != nil {
		t.Fatal(e)
	}
	if e = service.Register(); e != nil {
		t.Fatal(e)
	}
	if e = queue.StartPaused(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { c, cancel := context.WithTimeout(ctx, time.Second); defer cancel(); queue.Stop(c) }()
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
		_, token, e := security.CreateScopedToken(ctx, &u, u.Username, 1, scopes)
		if e != nil {
			t.Fatal(e)
		}
		tokens[string(u.Role)] = token
	}
	_, operateAdmin, e := security.CreateScopedToken(ctx, &users[0], "limited-admin", 1, []string{"operate"})
	if e != nil {
		t.Fatal(e)
	}
	tokens["limited-admin"] = operateAdmin
	h := chi.NewRouter()
	h.Use(security.Authenticate, security.CSRF)
	MountContent(h, service)
	request := func(role, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if role != "" {
			r.Header.Set("Authorization", "Bearer "+tokens[role])
		}
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	base := "/instances/alpha/content"
	for _, role := range []string{"viewer", "operator", "admin"} {
		for _, path := range []string{base, base + "/config"} {
			w := request(role, "GET", path, "")
			if w.Code != 200 || strings.Contains(w.Body.String(), "config-secret-canary") {
				t.Fatal(role, path, w.Code, w.Body)
			}
		}
	}
	for _, role := range []string{"", "viewer"} {
		w := request(role, "GET", base+"/config/text?path=config%2Fsecret.toml", "")
		want := 403
		if role == "" {
			want = 401
		}
		if w.Code != want || strings.Contains(w.Body.String(), "config-secret-canary") {
			t.Fatal(role, w.Code, w.Body)
		}
	}
	w := request("operator", "GET", base+"/config/text?path=config%2Fsecret.toml", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "config-secret-canary") {
		t.Fatal(w.Code, w.Body)
	}
	w = request("operator", "GET", base+"/config/text?path=config%2Fmissing.toml", "")
	if w.Code != 404 {
		t.Fatal("missing config is not a typed 404", w.Code, w.Body)
	}
	for _, role := range []string{"viewer", "operator", "limited-admin"} {
		w = request(role, "POST", base+"/uploads?filename=example.jar", "invalid")
		if w.Code != 403 {
			t.Fatal(role, "upload gate", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"action":"install","kind":"mod","upload_id":"private"}`, `{"action":"install","kind":"mod","trial":true}`} {
		w = request("operator", "POST", base+"/previews", body)
		if w.Code != 403 {
			t.Fatal("admin action gate", w.Code, w.Body)
		}
	}
	w = request("viewer", "POST", base+"/previews", `{"action":"disable","content_id":"unknown"}`)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body)
	}
	for _, body := range []string{`null`, `{"action":"disable","artifact_path":"/tmp/forged.jar"}`, `{"action":"disable"} {}`, `{"action":"config_write","config_path":"../server.properties","expected_sha256":"bad","text":"unsafe"}`} {
		w = request("operator", "POST", base+"/previews", body)
		if w.Code != 422 {
			t.Fatal(body, w.Code, w.Body)
		}
	}
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	f, _ := z.Create("Opaque.class")
	f.Write([]byte("inert"))
	z.Close()
	upload, e := service.StageUpload(ctx, "alpha", "opaque.jar", bytes.NewReader(b.Bytes()), users[0])
	if e != nil {
		t.Fatal(e)
	}
	preview, e := service.Preview(ctx, "alpha", content.Mutation{Action: "install", Kind: catalog.Mod, UploadID: upload.ID, Trial: true}, users[0])
	if e != nil || len(preview.Blockers) > 0 {
		t.Fatal(preview, e)
	}
	w = request("limited-admin", "POST", base+"/previews/"+preview.ID+"/apply", "")
	if w.Code != 403 {
		t.Fatal("administrator upload applied with operate scope", w.Code, w.Body)
	}
	w = request("admin", "POST", base+"/previews/"+preview.ID+"/apply", "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	var payload map[string]any
	if json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload["job"] == nil {
		t.Fatal(w.Body)
	}
}
