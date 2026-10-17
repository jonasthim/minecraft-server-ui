package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/selfupdate"
	"net/http/httptest"
	"strings"
	"testing"
)

type updateHTTP struct{ calls int }

func (f *updateHTTP) Status(context.Context) selfupdate.Status {
	f.calls++
	return selfupdate.Status{CurrentVersion: "0.0.0-dev", SchemaPolicy: "unchanged_schema_only"}
}
func (f *updateHTTP) Discover(context.Context, domain.User) (selfupdate.Discovery, error) {
	f.calls++
	return selfupdate.Discovery{CurrentVersion: "v0.0.1", Comparison: "no_release"}, nil
}
func (f *updateHTTP) Check(context.Context, string, domain.User) (selfupdate.CheckResult, error) {
	f.calls++
	return selfupdate.CheckResult{}, nil
}
func (f *updateHTTP) Stage(context.Context, string, domain.User) (selfupdate.Stage, error) {
	f.calls++
	return selfupdate.Stage{}, nil
}
func (f *updateHTTP) Apply(context.Context, selfupdate.ApplyInput, domain.User) error {
	f.calls++
	return nil
}
func TestSelfUpdateRoutesRequireAdminAndStrictInputs(t *testing.T) {
	f := &updateHTTP{}
	router := chi.NewRouter()
	MountSelfUpdate(router, f)
	for _, tc := range []struct {
		method, path, body string
		code               int
	}{{"GET", "/system/update", "", 200}, {"POST", "/system/update/discover", "", 200}, {"POST", "/system/update/check", `{"version":"v1.2.3"}`, 200}, {"POST", "/system/update/stage", `{"version":"v1.2.3"}`, 200}, {"POST", "/system/update/apply", `{"stage_id":"` + strings.Repeat("a", 64) + `","confirm":true}`, 202}} {
		for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if role != "" {
				r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: role}))
			}
			w := httptest.NewRecorder()
			before := f.calls
			router.ServeHTTP(w, r)
			want := tc.code
			if role == "" {
				want = 401
			} else if role != domain.RoleAdmin {
				want = 403
			}
			if w.Code != want || (want != tc.code && f.calls != before) {
				t.Fatal(role, tc.path, w.Code, w.Body.String())
			}
		}
	}
	for _, body := range []string{`{"version":"v1.2.3","path":"/tmp/evil"}`, `{"version":"latest"}`, `{"version":"v1.2.3"} {}`} {
		r := httptest.NewRequest("POST", "/system/update/check", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: domain.RoleAdmin}))
		w := httptest.NewRecorder()
		before := f.calls
		router.ServeHTTP(w, r)
		if w.Code != 422 || f.calls != before {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

func TestSelfUpdateDiscoveryRejectsUnexpectedInputs(t *testing.T) {
	f := &updateHTTP{}
	router := chi.NewRouter()
	MountSelfUpdate(router, f)
	for _, tc := range []struct{ path, body string }{
		{"/system/update/discover?repository=other/repo", ""},
		{"/system/update/discover", `{}`},
		{"/system/update/discover", `{"repository":"other/repo"}`},
		{"/system/update/discover", " "},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.ContentLength = -1 // Body validation must also cover streamed input.
		r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: domain.RoleAdmin}))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 422 || f.calls != 0 {
			t.Fatalf("unexpected discovery input accepted: %d %s", w.Code, w.Body.String())
		}
	}
}
