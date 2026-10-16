package main

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/api"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sharing /api/v1 must preserve public setup and protect jobs using that session.
func TestSharedAPIMountBootstrapsThenReadsJobs(t *testing.T) {
	database, err := db.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	identity := auth.NewService(database, auth.Config{InsecureCookies: true}, nil)
	queue := jobs.New(database, nil)
	defer queue.Stop(context.Background())
	handler := api.NewRouter(api.Dependencies{DB: database, Mount: func(r chi.Router) { mountAPI(r, identity, queue) }})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/jobs", nil))
	if response.Code != 401 {
		t.Fatalf("unprotected or missing jobs route %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/auth/status", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"needs_setup":true`) {
		t.Fatalf("public auth lost: %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest("POST", "/api/v1/auth/setup", strings.NewReader(`{"username":"admin","password":"valid-test-password-123"}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatalf("setup failed: %d %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	request = httptest.NewRequest("GET", "/api/v1/jobs", nil)
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"jobs":[]`) {
		t.Fatalf("authenticated jobs route missing: %d %s", response.Code, response.Body.String())
	}
}
