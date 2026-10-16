package auth

import (
	"context"
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func testHTTP(s *Service) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.Authenticate, s.CSRF)
		s.MountRoutes(r)
		r.With(RequireRole(domain.RoleViewer)).Get("/read", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		r.With(RequireRole(domain.RoleOperator)).Post("/operate", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
		r.With(RequireRole(domain.RoleAdmin)).Get("/admin", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	})
	return r
}
func request(h http.Handler, method, path, body string, cookie *http.Cookie, csrf, bearer, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if csrf != "" {
		r.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func bootstrapHTTP(t *testing.T, h http.Handler) (*http.Cookie, string) {
	t.Helper()
	w := request(h, "POST", "/api/v1/auth/setup", `{"username":"admin","password":"strong-password"}`, nil, "", "", "")
	if w.Code != 201 {
		t.Fatalf("setup status %d: %s", w.Code, w.Body)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == CookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("missing cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie lacks protections")
	}
	w = request(h, "GET", "/api/v1/auth/me", "", cookie, "", "", "")
	var me struct {
		CSRF string `json:"csrf_token"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &me); e != nil || me.CSRF == "" {
		t.Fatal("missing CSRF")
	}
	return cookie, me.CSRF
}
func TestHTTPRequiresCSRFAndRejectsCrossOrigin(t *testing.T) {
	s := newTestService(t)
	h := testHTTP(s)
	cookie, csrf := bootstrapHTTP(t, h)
	for _, tc := range []struct {
		token, origin string
		status        int
	}{{"", "", 403}, {csrf, "http://attacker.example", 403}, {csrf, "", 201}} {
		w := request(h, "POST", "/api/v1/users", `{"username":"viewer","password":"viewer-password","role":"viewer"}`, cookie, tc.token, "", tc.origin)
		if w.Code != tc.status {
			t.Fatalf("want %d got %d: %s", tc.status, w.Code, w.Body)
		}
	}
	w := request(h, "GET", "/api/v1/auth/me", "", cookie, "", "mcui_invalid", "")
	if w.Code != 401 {
		t.Fatal("invalid bearer fell back to cookie")
	}
	w = request(h, "POST", "/api/v1/auth/logout", "", cookie, csrf, "", "")
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	if request(h, "GET", "/api/v1/auth/me", "", cookie, "", "", "").Code != 401 {
		t.Fatal("logged-out cookie still authorized")
	}
}
func TestHTTPScopeCapsAndBrowserOnlyIdentity(t *testing.T) {
	s := newTestService(t)
	h := testHTTP(s)
	cookie, csrf := bootstrapHTTP(t, h)
	var secret string
	for _, scope := range []string{"read", "operate", "admin"} {
		w := request(h, "POST", "/api/v1/auth/tokens", `{"name":"automation","expires_in_days":30,"scopes":["`+scope+`"]}`, cookie, csrf, "", "")
		if w.Code != 201 {
			t.Fatalf("mint %d: %s", w.Code, w.Body)
		}
		var value struct {
			Secret string `json:"secret"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &value)
		secret = value.Secret
		if request(h, "GET", "/api/v1/read", "", nil, "", secret, "").Code != 204 {
			t.Fatal("read denied")
		}
		want := 403
		if scope != "read" {
			want = 204
		}
		if request(h, "POST", "/api/v1/operate", "{}", nil, "", secret, "").Code != want {
			t.Fatal("operation scope bypass")
		}
		want = 403
		if scope == "admin" {
			want = 204
		}
		if request(h, "GET", "/api/v1/admin", "", nil, "", secret, "").Code != want {
			t.Fatal("admin scope bypass")
		}
		if request(h, "POST", "/api/v1/auth/tokens", `{"name":"leaked-token"}`, nil, "", secret, "").Code != 403 {
			t.Fatal("bearer could mint token")
		}
	}
	w := request(h, "GET", "/api/v1/auth/tokens", "", cookie, "", "", "")
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("token secret returned by list")
	}
	var audit string
	if e := s.users.DB.QueryRow("SELECT COALESCE(group_concat(details_json||target),'') FROM audit_log").Scan(&audit); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(audit, secret) || strings.Contains(audit, "strong-password") {
		t.Fatal("secret entered audit")
	}
}
func TestFirstAdminAtomicAcrossServices(t *testing.T) {
	s := newTestService(t)
	other := NewService(s.users.DB, Config{InsecureCookies: true}, nil)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, svc := range []*Service{s, other} {
		wg.Add(1)
		go func(i int, svc *Service) {
			defer wg.Done()
			_, e := svc.Setup(context.Background(), httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), []string{"alpha", "beta"}[i], "strong-password", "", "")
			results <- e
		}(i, svc)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if domain.AsError(e).Code != domain.CodeSetupDone {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatalf("created %d first admins", success)
	}
	n, _ := s.users.Count(context.Background())
	if n != 1 {
		t.Fatal("extra bootstrap account")
	}
}
func TestLastAdminProtectedByDatabase(t *testing.T) {
	s := newTestService(t)
	h := testHTTP(s)
	bootstrapHTTP(t, h)
	if _, e := s.users.DB.Exec("UPDATE users SET role='viewer' WHERE username='admin'"); e == nil {
		t.Fatal("database allowed last admin demotion")
	}
	if _, e := s.users.DB.Exec("DELETE FROM users WHERE username='admin'"); e == nil {
		t.Fatal("database allowed last admin deletion")
	}
}
func TestPasswordVerifierRejectsUnsafeParameters(t *testing.T) {
	for _, hash := range []string{"$argon2id$v=19$m=1,t=0,p=0$YWJjZGVmZ2g$YWJj", "$argon2id$v=19$m=4294967295,t=3,p=4$YWJjZGVmZ2g$YWJj", "$argon2id$v=18$m=65536,t=3,p=4$YWJjZGVmZ2g$YWJj"} {
		if VerifyPassword(hash, "password") {
			t.Fatal("unsafe hash accepted")
		}
	}
}
