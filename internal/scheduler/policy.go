// Package scheduler persists recurring intent and admits typed feature jobs.
// It never launches a process, executes a shell or changes a pinned version.
package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonasthim/minecraft-server-ui/internal/console"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/robfig/cron/v3"
	_ "time/tzdata" // Windows installations need the same IANA rules as Linux.
)

const TimePolicy = "Cron uses the stored timezone: missing DST times are skipped; repeated times run twice. @every uses elapsed time. Missed runs are skipped without catch-up; overlapping occurrences are coalesced."
const DefaultCountdownSeconds = 300

// Input deliberately excludes creator identity, next-run and history fields.
// A missing countdown uses five minutes; an explicit zero is preserved.
type Input struct {
	Name             string                `json:"name"`
	Enabled          bool                  `json:"enabled"`
	Action           domain.ScheduleAction `json:"action"`
	Expression       string                `json:"expression"`
	Timezone         string                `json:"timezone"`
	Command          string                `json:"command,omitempty"`
	Announcement     string                `json:"announcement,omitempty"`
	BackupKind       domain.BackupKind     `json:"backup_kind,omitempty"`
	CountdownSeconds *int                  `json:"countdown_seconds,omitempty"`
}

// HostTimezone resolves the current host preference to a durable IANA name.
// Callers may configure an explicit name on hosts exposing only a Windows label.
// A mutable "Local" alias is never silently stored as a timezone.
func HostTimezone() string {
	candidates := []string{strings.TrimPrefix(os.Getenv("TZ"), ":"), time.Local.String()}
	if raw, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(raw)))
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(filepath.ToSlash(target), "/zoneinfo/"); ok {
			candidates = append(candidates, zone)
		}
	}
	for _, name := range candidates {
		if _, err := location(name); err == nil {
			return name
		}
	}
	return ""
}
func location(zone string) (*time.Location, error) {
	if zone == "" || zone == "Local" || len(zone) > 128 || strings.TrimSpace(zone) != zone {
		return nil, domain.E(domain.CodeValidationFailed, "choose an explicit IANA timezone; host timezone is unavailable")
	}
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return nil, domain.E(domain.CodeValidationFailed, "unknown IANA timezone")
	}
	return loc, nil
}
func parse(expression, zone string) (cron.Schedule, error) {
	loc, e := location(zone)
	if e != nil {
		return nil, e
	}
	if len(expression) == 0 || len(expression) > 256 || strings.TrimSpace(expression) != expression || strings.HasPrefix(expression, "TZ=") || strings.HasPrefix(expression, "CRON_TZ=") {
		return nil, domain.E(domain.CodeValidationFailed, "use five-field cron or a descriptor with the explicit timezone field")
	}
	if strings.HasPrefix(expression, "@every ") {
		d, e := time.ParseDuration(strings.TrimPrefix(expression, "@every "))
		if e != nil || d < time.Minute || d > 365*24*time.Hour || d%time.Second != 0 {
			return nil, domain.E(domain.CodeValidationFailed, "interval must be whole seconds between one minute and 365 days")
		}
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, e := p.Parse(expression)
	if e != nil {
		return nil, domain.E(domain.CodeValidationFailed, "invalid five-field cron or descriptor")
	}
	if spec, ok := schedule.(*cron.SpecSchedule); ok {
		spec.Location = loc
	}
	return schedule, nil
}
func Next(expression, zone string, after time.Time) (time.Time, error) {
	schedule, e := parse(expression, zone)
	if e != nil {
		return time.Time{}, e
	}
	next := schedule.Next(after).UTC()
	if next.IsZero() {
		return time.Time{}, domain.E(domain.CodeValidationFailed, "expression has no upcoming occurrence")
	}
	return next, nil
}
func textValid(text string, max int) bool {
	if text == "" || len(text) > max || !utf8.ValidString(text) || strings.TrimSpace(text) != text {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
func authorized(u domain.User) error {
	if u.ID <= 0 || u.Disabled || !u.Role.AtLeast(domain.RoleOperator) {
		return domain.E(domain.CodeForbidden, "active operator access required")
	}
	return nil
}
func validate(in Input, selection domain.VersionSelection, actor domain.User, host string, now time.Time) (domain.Schedule, error) {
	out := domain.Schedule{Name: in.Name, Enabled: in.Enabled, Action: in.Action, Expression: in.Expression, Timezone: in.Timezone, Command: in.Command, Announcement: in.Announcement, BackupKind: in.BackupKind, CountdownSeconds: DefaultCountdownSeconds}
	if e := authorized(actor); e != nil {
		return out, e
	}
	if !textValid(in.Name, 80) {
		return out, domain.E(domain.CodeValidationFailed, "schedule name must contain 1–80 bytes of plain text")
	}
	if out.Timezone == "" {
		out.Timezone = host
	}
	next, e := Next(out.Expression, out.Timezone, now)
	if e != nil {
		return out, e
	}
	if out.Enabled {
		out.NextRunAt = &next
	}
	if in.CountdownSeconds != nil {
		out.CountdownSeconds = *in.CountdownSeconds
		if in.Action != domain.ScheduleRestart || out.CountdownSeconds < 0 || out.CountdownSeconds > 3600 {
			return out, domain.E(domain.CodeValidationFailed, "restart countdown must be between 0 and 3600 seconds")
		}
	}
	if in.Command != "" && in.Action != domain.ScheduleCommand || in.Announcement != "" && in.Action != domain.ScheduleAnnouncement || in.BackupKind != "" && in.Action != domain.ScheduleBackup {
		return out, domain.E(domain.CodeValidationFailed, "action contains unrelated fields")
	}
	switch in.Action {
	case domain.ScheduleBackup:
		if in.BackupKind != domain.BackupWorld && in.BackupKind != domain.BackupFull {
			return out, domain.E(domain.CodeValidationFailed, "backup scope must be world or full")
		}
	case domain.ScheduleCommand:
		if _, e := console.ValidateCommand(selection, actor.Role, in.Command); e != nil {
			return out, e
		}
	case domain.ScheduleAnnouncement:
		if _, e := console.ValidateCommand(selection, actor.Role, "say "+in.Announcement); e != nil {
			return out, e
		}
	case domain.ScheduleRestart, domain.ScheduleUpdate:
	default:
		return out, domain.E(domain.CodeValidationFailed, "unknown schedule action")
	}
	return out, nil
}
