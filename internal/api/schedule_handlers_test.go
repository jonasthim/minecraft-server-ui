package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"github.com/jonasthim/minecraft-server-ui/internal/auth"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/scheduler"
	"net/http/httptest"
	"strings"
	"testing"
)

type schedulesHTTP struct{ calls int }

func (f *schedulesHTTP) List(context.Context, string) ([]scheduler.Record, error) {
	f.calls++
	return []scheduler.Record{}, nil
}
func (f *schedulesHTTP) Save(context.Context, string, string, scheduler.Input, domain.User) (scheduler.Record, error) {
	f.calls++
	return scheduler.Record{}, nil
}
func (f *schedulesHTTP) Delete(context.Context, string, string, domain.User) error {
	f.calls++
	return nil
}
func (f *schedulesHTTP) History(context.Context, string, string) ([]scheduler.Run, error) {
	f.calls++
	return []scheduler.Run{}, nil
}
func (f *schedulesHTTP) Recovery(context.Context, string) (scheduler.RecoveryStatus, error) {
	f.calls++
	return scheduler.RecoveryStatus{Attempts: []scheduler.Attempt{}}, nil
}
func (f *schedulesHTTP) ConfigureRecovery(context.Context, string, bool, domain.User) (scheduler.RecoveryStatus, error) {
	f.calls++
	return scheduler.RecoveryStatus{}, nil
}
func (f *schedulesHTTP) DefaultTimezone() string { return "UTC" }
func (f *schedulesHTTP) AvailableActions() []domain.ScheduleAction {
	return []domain.ScheduleAction{domain.ScheduleCommand}
}
func TestScheduleHTTPRolesAndStrictInput(t *testing.T) {
	f := &schedulesHTTP{}
	router := chi.NewRouter()
	MountSchedules(router, f)
	for _, tc := range []struct {
		method, path, body string
		minimum            domain.Role
		success            int
	}{
		{"GET", "/instances/alpha/schedules", "", domain.RoleViewer, 200},
		{"POST", "/instances/alpha/schedules", `{"name":"List","action":"command","expression":"@hourly","command":"list"}`, domain.RoleOperator, 201},
		{"PUT", "/instances/alpha/schedules/one", `{"name":"List","action":"command","expression":"@hourly","command":"list"}`, domain.RoleOperator, 200},
		{"DELETE", "/instances/alpha/schedules/one", "", domain.RoleOperator, 204},
		{"GET", "/instances/alpha/recovery", "", domain.RoleViewer, 200},
		{"PUT", "/instances/alpha/recovery", `{"enabled":true}`, domain.RoleAdmin, 200},
	} {
		for _, role := range []domain.Role{"", domain.RoleViewer, domain.RoleOperator, domain.RoleAdmin} {
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if role != "" {
				r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: role}))
			}
			w := httptest.NewRecorder()
			before := f.calls
			router.ServeHTTP(w, r)
			want := tc.success
			if role == "" {
				want = 401
			} else if !role.AtLeast(tc.minimum) {
				want = 403
			}
			if w.Code != want || (want == 401 || want == 403) && f.calls != before {
				t.Fatal(tc.path, role, w.Code, w.Body.String())
			}
		}
	}
	for _, body := range []string{`{"enabled":true,"actor_id":42}`, `{"enabled":true} {"enabled":false}`} {
		r := httptest.NewRequest("PUT", "/instances/alpha/recovery", strings.NewReader(body))
		r = r.WithContext(auth.WithUser(r.Context(), &domain.User{ID: 1, Role: domain.RoleAdmin}))
		w := httptest.NewRecorder()
		before := f.calls
		router.ServeHTTP(w, r)
		if w.Code != 422 || f.calls != before {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
