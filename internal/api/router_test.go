package api

import (
	"context"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRouterHealthAndSPAAreDistinct(t *testing.T) {
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	h := NewRouter(Dependencies{DB: database, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Web: fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app.js": {Data: []byte("export {}")}}})
	for _, tc := range []struct {
		path     string
		code     int
		contains string
	}{{"/healthz", 200, `"status":"ok"`}, {"/api/missing", 404, `"code":"not_found"`}, {"/health", 404, `"code":"not_found"`}, {"/assets/missing.js", 404, ""}, {"/", 200, "<html>app</html>"}, {"/instances/main", 200, "<html>app</html>"}} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.contains) {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	database.Close()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != 503 {
		t.Fatalf("closed database health=%d", rec.Code)
	}
}
