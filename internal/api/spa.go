package api

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

func spaHandler(frontend fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		// Only actual browser application routes receive SPA fallback. Misspelled
		// health/API routes and missing assets remain observable 404 responses.
		appRoute := p == "/" || p == "/login" || p == "/setup" || p == "/instances" || strings.HasPrefix(p, "/instances/") || p == "/account" || p == "/users" || p == "/audit" || p == "/settings" || strings.HasPrefix(p, "/settings/")
		asset := strings.HasPrefix(p, "/assets/") || p == "/favicon.svg"
		if frontend == nil || (!appRoute && !asset) || (r.Method != "GET" && r.Method != "HEAD") {
			WriteError(w, domain.NotFound("resource"))
			return
		}
		if asset {
			clean := strings.TrimPrefix(path.Clean(p), "/")
			if _, err := fs.Stat(frontend, clean); err != nil {
				WriteError(w, domain.NotFound("asset"))
				return
			}
			http.FileServer(http.FS(frontend)).ServeHTTP(w, r)
			return
		}
		html, err := fs.ReadFile(frontend, "index.html")
		if err != nil {
			WriteError(w, domain.E(domain.CodeUnavailable, "Build the frontend with make web"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		if r.Method != "HEAD" {
			_, _ = w.Write(html)
		}
	}
}
