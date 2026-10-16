package scheduler

import (
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, e := time.Parse(time.RFC3339, s)
	if e != nil {
		panic(e)
	}
	return t
}
func TestCronTimezoneDSTAndGrammar(t *testing.T) {
	for _, tc := range []struct{ expr, zone, after, want string }{
		{"30 2 * * *", "Europe/Stockholm", "2026-03-28T02:00:00Z", "2026-03-30T00:30:00Z"},
		{"30 2 * * *", "Europe/Stockholm", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"},
		{"@daily", "Europe/Stockholm", "2026-10-16T12:00:00Z", "2026-10-16T22:00:00Z"},
		{"@every 2h", "Europe/Stockholm", "2026-03-29T00:30:00Z", "2026-03-29T02:30:00Z"},
	} {
		next, e := Next(tc.expr, tc.zone, at(tc.after))
		if e != nil || !next.Equal(at(tc.want)) {
			t.Errorf("%s next=%v err=%v want=%s", tc.expr, next, e, tc.want)
		}
	}
	for _, expr := range []string{"* * * * * *", "@every 10s", "@every 9000h", "CRON_TZ=UTC * * * * *", "TZ=UTC * * * * *", "0 0 31 2 *"} {
		if _, e := Next(expr, "UTC", at("2026-10-16T12:00:00Z")); e == nil {
			t.Errorf("accepted %q", expr)
		}
	}
	if _, e := Next("@daily", "Local", time.Now()); e == nil {
		t.Fatal("accepted mutable Local zone")
	}
}
func TestValidateActionAndCountdown(t *testing.T) {
	selection := domain.VersionSelection{Flavor: domain.FlavorVanilla, MinecraftVersion: "1.21.9"}
	user := domain.User{ID: 1, Role: domain.RoleOperator}
	in := Input{Name: "Daily restart", Action: domain.ScheduleRestart, Expression: "0 3 * * *"}
	out, e := validate(in, selection, user, "Europe/Stockholm", at("2026-10-16T12:00:00Z"))
	if e != nil || out.CountdownSeconds != 300 || out.Timezone != "Europe/Stockholm" || out.Enabled {
		t.Fatal(out, e)
	}
	zero := 0
	in.CountdownSeconds = &zero
	out, e = validate(in, selection, user, "UTC", time.Now())
	if e != nil || out.CountdownSeconds != 0 {
		t.Fatal(out, e)
	}
	in.Command = "stop"
	if _, e = validate(in, selection, user, "UTC", time.Now()); e == nil {
		t.Fatal("accepted irrelevant unsafe command")
	}
	in = Input{Name: "Safe", Action: domain.ScheduleCommand, Expression: "@hourly", Command: "seed"}
	if _, e = validate(in, selection, user, "UTC", time.Now()); domain.AsError(e).Code != domain.CodeForbidden {
		t.Fatal(e)
	}
	in.Command = "list"
	if _, e = validate(in, selection, user, "UTC", time.Now()); e != nil {
		t.Fatal(e)
	}
	in.Action = domain.ScheduleAnnouncement
	in.Command = ""
	in.Announcement = "Hello\nstop"
	if _, e = validate(in, selection, user, "UTC", time.Now()); e == nil {
		t.Fatal("accepted newline announcement")
	}
}
