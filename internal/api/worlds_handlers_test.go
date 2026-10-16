package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/instance"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"github.com/jonasthim/minecraft-server-ui/internal/world"
)

func TestWorldHTTPRolesTokensAndCaptureFailureBeforeSuccess(t *testing.T) {
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
	instances := instance.New(database, paths, instanceTestGuard{})
	draft := instance.Draft{Instance: domain.Instance{ID: "alpha", Name: "Alpha", Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9", MemoryMiB: 1024, GamePort: 25565}, Config: instance.DefaultConfig()}
	if _, e = instances.Create(ctx, domain.User{ID: 1, Role: domain.RoleAdmin}, draft); e != nil {
		t.Fatal(e)
	}
	dirs, e := paths.For("alpha")
	if e != nil {
		t.Fatal(e)
	}
	worldDir := filepath.Join(dirs.Server, draft.Config.LevelName)
	if e = os.MkdirAll(worldDir, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(worldDir, "level.dat"), []byte("saved"), 0600); e != nil {
		t.Fatal(e)
	}
	queue := jobs.New(database, nil)
	if e = queue.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer queue.Stop(ctx)
	s := world.New(database, instances, paths, queue)
	authService := auth.NewService(database, auth.Config{BaseURL: "http://example.com", InsecureCookies: true}, nil)
	r := chi.NewRouter()
	r.Use(authService.Authenticate, authService.CSRF)
	MountWorlds(r, s)
	request := func(path, token string, user *domain.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if user != nil {
			req = req.WithContext(auth.WithUser(req.Context(), user))
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, tc := range []struct {
		role                   domain.Role
		wantRead, wantDownload int
	}{{"", 401, 401}, {domain.RoleViewer, 200, 403}, {domain.RoleOperator, 200, 200}, {domain.RoleAdmin, 200, 200}} {
		var user *domain.User
		if tc.role != "" {
			user = &domain.User{ID: 1, Role: tc.role}
		}
		for path, want := range map[string]int{"/instances/alpha/world": tc.wantRead, "/instances/alpha/world/download": tc.wantDownload} {
			w := request(path, "", user)
			if w.Code != want {
				t.Fatalf("%s %s: %d %s", tc.role, path, w.Code, w.Body)
			}
			if want == 200 && strings.HasSuffix(path, "download") {
				if w.Header().Get("Content-Type") != "application/zip" || !strings.Contains(w.Header().Get("Content-Disposition"), "alpha-world.zip") || w.Header().Get("X-World-Consistency") != "best-effort-live-copy" || len(w.Body.Bytes()) < 4 || string(w.Body.Bytes()[:2]) != "PK" {
					t.Fatal(w.Header(), w.Body.String())
				}
			}
		}
	}
	setupW := httptest.NewRecorder()
	setupReq := httptest.NewRequest("POST", "/auth/setup", nil)
	if _, e = authService.Setup(ctx, setupW, setupReq, "admin", "very-strong-password", "", ""); e != nil {
		t.Fatal(e)
	}
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
		user, e := authService.Create(ctx, "test-"+string(role), "very-strong-password", "", "", role)
		if e != nil {
			t.Fatal(e)
		}
		for _, scope := range []string{"read", "operate", "admin"} {
			if scope == "admin" && role != domain.RoleAdmin || scope == "operate" && role == domain.RoleViewer {
				continue
			}
			token, secret, e := authService.CreateScopedToken(ctx, user, "test", 1, []string{scope})
			if e != nil {
				t.Fatal(e)
			}
			want := 200
			if role == domain.RoleViewer {
				want = 403
			}
			w := request("/instances/alpha/world/download", secret, nil)
			if w.Code != want {
				t.Fatal(role, scope, w.Code, w.Body.String())
			}
			if e = authService.RevokeToken(ctx, user.ID, token.ID); e != nil {
				t.Fatal(e)
			}
			if w = request("/instances/alpha/world/download", secret, nil); w.Code != 401 {
				t.Fatal("revoked token accepted", w.Code)
			}
		}
		token, secret, e := authService.CreateScopedToken(ctx, user, "expiry", 1, []string{"read"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = database.Exec(`UPDATE api_tokens SET expires_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), token.ID); e != nil {
			t.Fatal(e)
		}
		if w := request("/instances/alpha/world/download", secret, nil); w.Code != 401 {
			t.Fatal("expired token accepted", w.Code)
		}
		_, secret, e = authService.CreateScopedToken(ctx, user, "disabled", 1, []string{"read"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = database.Exec(`UPDATE users SET disabled=1 WHERE id=?`, user.ID); e != nil {
			t.Fatal(e)
		}
		if w := request("/instances/alpha/world/download", secret, nil); w.Code != 401 {
			t.Fatal("disabled token owner accepted", w.Code)
		}
	}
	writer := &worldTransferWriter{ResponseRecorder: httptest.NewRecorder(), queue: queue}
	transferRequest := httptest.NewRequest("GET", "/instances/alpha/world/download", nil)
	transferRequest = transferRequest.WithContext(auth.WithUser(transferRequest.Context(), &domain.User{ID: 1, Role: domain.RoleOperator}))
	r.ServeHTTP(writer, transferRequest)
	if !writer.checked || writer.admissionErr != nil || writer.Code != 200 {
		t.Fatal("capture reservation extended into transfer", writer.checked, writer.admissionErr, writer.Code)
	}
	held, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() { done <- queue.WithIdle(ctx, "alpha", func() error { close(held); <-release; return nil }) }()
	<-held
	busy := request("/instances/alpha/world/download", "", &domain.User{ID: 1, Role: domain.RoleOperator})
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if busy.Code != 409 || strings.HasPrefix(busy.Body.String(), "PK") {
		t.Fatal("capture bypassed queue reservation", busy.Code, busy.Body.String())
	}
	w := request("/instances/alpha/world", "", &domain.User{ID: 1, Role: domain.RoleViewer})
	var response struct {
		World world.Discovery `json:"world"`
	}
	if e = json.Unmarshal(w.Body.Bytes(), &response); e != nil || response.World.State != "present" {
		t.Fatal(w.Body.String(), e)
	}
	if e = os.Symlink(filepath.Join(dirs.Server, "server.properties"), filepath.Join(worldDir, "secret.dat")); e == nil {
		w = request("/instances/alpha/world/download", "", &domain.User{ID: 1, Role: domain.RoleOperator})
		if w.Code == 200 || w.Header().Get("Content-Type") == "application/zip" || strings.HasPrefix(w.Body.String(), "PK") {
			t.Fatal("unsafe capture sent HTTP success", w.Code, w.Body.String())
		}
	}
}

// The first HTTP body write occurs only after Capture releases queue admission.
type worldTransferWriter struct {
	*httptest.ResponseRecorder
	queue        *jobs.Service
	checked      bool
	admissionErr error
}

func (w *worldTransferWriter) Write(p []byte) (int, error) {
	if !w.checked {
		w.checked = true
		w.admissionErr = w.queue.WithIdle(context.Background(), "alpha", func() error { return nil })
	}
	return w.ResponseRecorder.Write(p)
}
