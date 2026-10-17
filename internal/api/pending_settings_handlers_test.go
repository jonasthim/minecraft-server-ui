package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
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

func TestPendingSettingsHTTPRejectsCompactSerializedExpansionAtomically(t *testing.T) {
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
	admin := domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
	guard := pendingHTTPGuard{}
	service := instance.New(database, paths, guard, instance.WithPendingSettingsGuard(guard))
	detail, err := service.Create(t.Context(), admin, instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	MountInstances(router, service)
	request := func(method string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/instances/alpha/pending-settings", bytes.NewReader(body))
		req = req.WithContext(auth.WithUser(req.Context(), &admin))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	encode := func(v any) []byte {
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(v); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	in := instance.SavePendingSettings{PendingSettingsDraft: instance.PendingSettingsDraft{Name: "Prior draft", MemoryMiB: 2048, Config: detail.Config}, ExpectedInstanceUpdatedAt: detail.Instance.UpdatedAt.Format(time.RFC3339Nano)}
	saved := request("PUT", encode(in))
	if saved.Code != 200 {
		t.Fatalf("initial save: %d %s", saved.Code, saved.Body)
	}
	var before instance.PendingSettings
	if err := json.Unmarshal(saved.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	var auditsBefore int
	if err := database.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&auditsBefore); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = before.Revision
	in.Name = "Expanded replacement"
	in.Config.Extra = map[string]string{}
	for i := range 100 {
		in.Config.Extra[fmt.Sprintf("mod.option.%03d", i)] = strings.Repeat("<", 5000)
	}
	body := encode(in)
	stored, err := json.Marshal(in.PendingSettingsDraft)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) >= 1<<20 || len(stored) <= 2<<20 {
		t.Fatalf("fixture must fit HTTP body but exceed stored budget: request=%d stored=%d", len(body), len(stored))
	}
	t.Logf("compact request bytes=%d serialized draft bytes=%d", len(body), len(stored))
	response := request("PUT", body)
	if response.Code != 422 {
		t.Fatalf("oversized serialized draft must fail validation: status=%d response_bytes=%d", response.Code, response.Body.Len())
	}
	var failure struct {
		Error struct {
			Code domain.ErrorCode `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || failure.Error.Code != domain.CodeValidationFailed {
		t.Fatalf("expected ordinary validation error: %s %v", response.Body, err)
	}
	response = request("GET", nil)
	var after instance.PendingSettings
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &after) != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("refusal changed prior draft: %d %s", response.Code, response.Body)
	}
	var auditsAfter int
	if err := database.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore {
		t.Fatalf("refusal changed audits: before=%d after=%d err=%v", auditsBefore, auditsAfter, err)
	}
	response = request("DELETE", encode(map[string]int64{"expected_revision": before.Revision}))
	if response.Code != 200 {
		t.Fatalf("prior draft no longer discardable: %d %s", response.Code, response.Body)
	}
	var discarded instance.PendingSettings
	if err := json.Unmarshal(response.Body.Bytes(), &discarded); err != nil || discarded.Draft != nil || discarded.Revision != before.Revision+1 {
		t.Fatalf("discard outcome: %s %v", response.Body, err)
	}
	// A smaller HTML-sensitive draft still makes the complete save/read/discard
	// round trip; the budget rejects only the oversized stored representation.
	in.ExpectedRevision = discarded.Revision
	in.Config.Extra = map[string]string{"mod.option": strings.Repeat("<>&", 2000)}
	response = request("PUT", encode(in))
	if response.Code != 200 {
		t.Fatalf("bounded save: %d %s", response.Code, response.Body)
	}
	response = request("GET", nil)
	var bounded instance.PendingSettings
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &bounded) != nil || bounded.Draft == nil || !reflect.DeepEqual(*bounded.Draft, in.PendingSettingsDraft) {
		t.Fatalf("bounded read: %d %s", response.Code, response.Body)
	}
	response = request("DELETE", encode(map[string]int64{"expected_revision": bounded.Revision}))
	if response.Code != 200 {
		t.Fatalf("bounded discard: %d %s", response.Code, response.Body)
	}
}
