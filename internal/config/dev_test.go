package config

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/jonasthim/minecraft-server-ui/internal/auth"
)

// Exercise the configuration actually emitted by the documented development
// command, then pass a Vite-origin request through the production CSRF guard.
func TestDevelopmentCommandAcceptsViteOrigin(t *testing.T) {
	if _, err := exec.LookPath("make"); errors.Is(err, exec.ErrNotFound) {
		t.Skip("Makefile command contract requires make; direct configuration/CSRF regression runs separately")
	} else if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MINECRAFT_UI_ADDR", "127.0.0.1:8080")
	t.Setenv("MINECRAFT_UI_BASE_URL", "")
	t.Setenv("MINECRAFT_UI_INSECURE_COOKIES", "")
	command := exec.Command("make", "--no-print-directory", "-n", "dev-backend", "GO=go")
	command.Dir = "../.."
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range strings.Fields(string(output)) {
		key, value, ok := strings.Cut(word, "=")
		if ok && strings.HasPrefix(key, "MINECRAFT_UI_") {
			t.Setenv(key, value)
		}
	}
	assertViteOrigin(t)
}

// This guard runs on every platform, including native Windows without make.
func TestConfiguredViteOriginCSRF(t *testing.T) {
	t.Setenv("MINECRAFT_UI_ADDR", "127.0.0.1:8080")
	t.Setenv("MINECRAFT_UI_BASE_URL", "http://127.0.0.1:5173")
	t.Setenv("MINECRAFT_UI_INSECURE_COOKIES", "")
	assertViteOrigin(t)
}

func assertViteOrigin(t *testing.T) {
	t.Helper()
	cfg, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://127.0.0.1:5173" {
		t.Fatalf("dev origin = %q; Vite uses http://127.0.0.1:5173", cfg.BaseURL)
	}
	service := auth.NewService(nil, auth.Config{BaseURL: cfg.BaseURL, InsecureCookies: cfg.InsecureCookies}, nil)
	handler := service.CSRF(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/v1/auth/setup", strings.NewReader(`{}`))
	request.Header.Set("Origin", "http://127.0.0.1:5173")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("Vite mutation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request.Header.Set("Origin", "https://untrusted.example")
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("untrusted origin status=%d", recorder.Code)
	}
}
