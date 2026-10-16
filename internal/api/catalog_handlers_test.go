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
	"github.com/jonasthim/minecraft-server-ui/internal/catalog"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
)

func TestCatalogRolesPrivateKeyAndAudit(t *testing.T) {
	ctx := context.Background()
	d, e := db.OpenMemory(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	s, e := catalog.New(catalog.Config{DB: d, ArtifactDir: t.TempDir(), UserAgent: "catalog-test"})
	if e != nil {
		t.Fatal(e)
	}
	request := func(role domain.Role, method, path, body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if role != "" {
					r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Username: "admin", Role: role}))
				}
				next.ServeHTTP(w, r)
			})
		})
		MountCatalog(r, s)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rr
	}
	for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator} {
		rr := request(role, "PUT", "/catalog/curseforge-key", `{"key":"secret-not-for-viewers"}`)
		want := 403
		if role == "" {
			want = 401
		}
		if rr.Code != want {
			t.Fatal(role, rr.Code, rr.Body)
		}
	}
	rr := request(domain.RoleAdmin, "PUT", "/catalog/curseforge-key", `{"key":"secret-not-for-viewers"}`)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "secret-not-for-viewers") {
		t.Fatal(rr.Code, rr.Body)
	}
	rr = request(domain.RoleViewer, "GET", "/catalog/providers", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "secret-not-for-viewers") || !strings.Contains(rr.Body.String(), `"enabled":true`) {
		t.Fatal(rr.Code, rr.Body)
	}
	var action, details string
	if e := d.QueryRowContext(ctx, "SELECT action,details_json FROM audit_log ORDER BY id DESC LIMIT 1").Scan(&action, &details); e != nil || action != "catalog.curseforge_key.replace" || details != "{}" {
		t.Fatal(action, details, e)
	}
	rr = request(domain.RoleAdmin, "PUT", "/catalog/curseforge-key", `{"key":""}`)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatal(rr.Code, rr.Body)
	}
	rr = request(domain.RoleViewer, "GET", "/catalog/curseforge/search?kind=mod", "")
	if rr.Code != 503 {
		t.Fatal(rr.Code, rr.Body)
	}
	for _, path := range []string{"/catalog/modrinth/search?limit=invalid", "/catalog/modrinth/search?offset=-1", "/catalog/modrinth/search?limit=51", "/catalog/unknown/search?kind=mod"} {
		rr = request(domain.RoleViewer, "GET", path, "")
		if rr.Code != 422 {
			t.Fatal(path, rr.Code, rr.Body)
		}
	}
	for _, body := range []string{`{"key":"x","unknown":1}`, `{"key":"` + strings.Repeat("a", 600) + `"}`, `{"key":"x"} {"key":"y"}`} {
		rr = request(domain.RoleAdmin, "PUT", "/catalog/curseforge-key", body)
		if rr.Code != 422 {
			t.Fatal(rr.Code, rr.Body)
		}
	}
	var rows int
	d.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log").Scan(&rows)
	if rows != 2 {
		t.Fatal("unexpected config mutations", rows)
	}
	var result struct {
		Provider string `json:"provider"`
		Enabled  bool   `json:"enabled"`
		Reason   string `json:"reason"`
	}
	rr = request(domain.RoleAdmin, "PUT", "/catalog/curseforge-key", `{"key":""}`)
	if e = json.Unmarshal(rr.Body.Bytes(), &result); e != nil || result.Provider != "curseforge" || result.Enabled {
		t.Fatal(result, e)
	}
}
