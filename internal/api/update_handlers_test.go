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
)

func TestUpdateRoutesRequireRolesAndExistingPATScopes(t *testing.T) {
	database, err := db.OpenMemory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	security := auth.NewService(database, auth.Config{}, nil)
	admin := domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
	viewer := domain.User{ID: 2, Username: "viewer", Role: domain.RoleViewer}
	for _, user := range []domain.User{admin, viewer} {
		if _, err = database.Exec("INSERT INTO users(id,username,role,created_at,updated_at)VALUES(?,?,?,'now','now')", user.ID, user.Username, user.Role); err != nil {
			t.Fatal(err)
		}
	}
	tokens := map[string]string{}
	for label, scopes := range map[string][]string{"admin-read": {"read"}, "admin-operate": {"operate"}} {
		_, token, err := security.CreateScopedToken(context.Background(), &admin, label, 1, scopes)
		if err != nil {
			t.Fatal(err)
		}
		tokens[label] = token
	}
	_, viewerToken, err := security.CreateScopedToken(t.Context(), &viewer, "viewer-read", 1, []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(security.Authenticate, security.CSRF)
	MountUpdates(router, nil)
	for _, test := range []struct {
		token, method, path, body string
		want                      int
	}{
		{"", "GET", "/instances/alpha/updates", "", 401},
		{tokens["admin-read"], "POST", "/instances/alpha/updates/previews", `{"mode":"automatic"}`, 403},
		{viewerToken, "GET", "/instances/alpha/updates/previews", "", 403},
		{tokens["admin-operate"], "POST", "/instances/alpha/updates/rollbacks/previews", `{"transaction_id":"parent"}`, 403},
		{tokens["admin-operate"], "POST", "/instances/alpha/updates/rollbacks/preview/apply", `{"confirmation":"alpha","acknowledge_world_rewind":true}`, 403},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		if test.token != "" {
			request.Header.Set("Authorization", "Bearer "+test.token)
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatal(test.path, recorder.Code, recorder.Body)
		}
	}
}
func TestUpdateRequestRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, raw := range []string{`{"mode":"manual","target_path":"/tmp/executable"}`, `{} {}`, strings.Repeat("x", (4<<20)+1)} {
		var input struct {
			Mode string `json:"mode"`
		}
		request := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		if err := decodeUpdate(request, &input); err == nil {
			t.Fatal("unsafe request decoded")
		}
	}
}
