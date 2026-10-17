package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
)

type pendingHTTPGuard struct{ instanceTestGuard }

func (pendingHTTPGuard) WithPendingSettings(_ context.Context, _ string, fn func() error) error {
	return fn()
}

func TestPendingSettingsHTTPContractAndAuthority(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	paths, err := instance.NewPaths(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(1,'admin','admin','2026-10-17','2026-10-17')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at) VALUES(2,'other-admin','admin','2026-10-17','2026-10-17')`); err != nil {
		t.Fatal(err)
	}
	admin := domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
	guard := pendingHTTPGuard{}
	service := instance.New(database, paths, guard, instance.WithPendingSettingsGuard(guard))
	detail, err := service.Create(t.Context(), admin, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	MountInstances(router, service)
	request := func(method, body string, role domain.Role) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/instances/alpha/pending-settings", strings.NewReader(body))
		if role != "" {
			u := admin
			u.Role = role
			req = req.WithContext(auth.WithUser(req.Context(), &u))
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	configJSON, _ := json.Marshal(detail.Config)
	var config map[string]any
	if err := json.Unmarshal(configJSON, &config); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"name": "Desired", "memory_mib": 2048, "config": config, "expected_revision": 0, "expected_instance_updated_at": detail.Instance.UpdatedAt.Format(time.RFC3339Nano)}
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		wantRead := 200
		if role == "" {
			wantRead = 401
		}
		if w := request("GET", "", role); w.Code != wantRead {
			t.Fatalf("GET role %s: %d %s", role, w.Code, w.Body)
		}
		if role == domain.RoleAdmin {
			continue
		}
		wantWrite := 403
		if role == "" {
			wantWrite = 401
		}
		for _, method := range []string{"PUT", "DELETE"} {
			if w := request(method, encode(body), role); w.Code != wantWrite {
				t.Fatalf("%s role %s: %d %s", method, role, w.Code, w.Body)
			}
		}
	}
	// Omitting an explicit boolean must not silently turn off authentication.
	for _, key := range []string{"online_mode", "pvp", "extra", "level_seed"} {
		original := config[key]
		for _, null := range []bool{false, true} {
			delete(config, key)
			if null {
				config[key] = nil
			}
			if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 422 {
				t.Fatalf("incomplete config %s null=%v: %d %s", key, null, w.Code, w.Body)
			}
		}
		config[key] = original
	}
	for _, key := range []string{"name", "memory_mib", "expected_revision", "expected_instance_updated_at", "config"} {
		original := body[key]
		delete(body, key)
		if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 422 {
			t.Fatalf("missing %s: %d %s", key, w.Code, w.Body)
		}
		body[key] = original
	}
	for _, invalid := range []any{nil, -1, 1.5, int64(9007199254740992)} {
		body["expected_revision"] = invalid
		if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 422 {
			t.Fatalf("invalid revision %v: %d %s", invalid, w.Code, w.Body)
		}
	}
	body["expected_revision"] = 0
	config["rcon.password"] = "must-not-accept"
	if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 422 {
		t.Fatalf("unknown config field: %d %s", w.Code, w.Body)
	}
	delete(config, "rcon.password")
	w := request("PUT", encode(body), domain.RoleAdmin)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var saved instance.PendingSettings
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.Draft == nil || saved.Draft.Name != "Desired" || saved.Revision < 1 {
		t.Fatalf("saved response: %s (%v)", w.Body, err)
	}
	active, err := service.Get(t.Context(), "alpha")
	if err != nil || active.Instance.Name != "Alpha" || active.Instance.MemoryMiB != 1024 || !active.Config.OnlineMode {
		t.Fatalf("draft altered active settings: %+v %v", active, err)
	}
	body["name"] = "Conflicting"
	if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 409 {
		t.Fatalf("stale save: %d %s", w.Code, w.Body)
	}
	if w := request("DELETE", `{"expected_revision":0}`, domain.RoleAdmin); w.Code != 409 {
		t.Fatalf("stale discard: %d %s", w.Code, w.Body)
	}
	if _, err := database.Exec(`UPDATE users SET role='viewer' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	body["expected_revision"] = saved.Revision
	if w := request("PUT", encode(body), domain.RoleAdmin); w.Code != 403 {
		t.Fatalf("revoked admin save: %d %s", w.Code, w.Body)
	}
	if w := request("DELETE", encode(map[string]any{"expected_revision": saved.Revision}), domain.RoleAdmin); w.Code != 403 {
		t.Fatalf("revoked admin discard: %d %s", w.Code, w.Body)
	}
	if _, err := database.Exec(`UPDATE users SET role='admin' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	w = request("DELETE", encode(map[string]any{"expected_revision": saved.Revision}), domain.RoleAdmin)
	var discarded instance.PendingSettings
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &discarded) != nil || discarded.Draft != nil || discarded.Revision <= saved.Revision {
		t.Fatalf("discard must retain revision: %d %s", w.Code, w.Body)
	}
}
