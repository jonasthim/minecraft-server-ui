package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/events"
	"regexp"
	"strings"
	"time"
)

// Execution carries private inputs and the durable public journal. Handlers
// must respect ctx, and must use an independent bounded context for critical
// cleanup (such as save-on). Return only after cleanup has actually finished.
type Execution struct {
	Job     domain.Job
	Payload json.RawMessage
	service *Service
	secrets []string
}

func (e *Execution) CleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// Phase journals a machine phase identifier before performing that phase.
func (e *Execution) Phase(ctx context.Context, phase string, progress int) error {
	if !identifier.MatchString(phase) || progress < 0 || progress > 100 {
		return domain.E(domain.CodeValidationFailed, "invalid job progress")
	}
	s := e.service
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET phase=?,progress=? WHERE id=? AND state='running'`, phase, progress, e.Job.ID)
	if err != nil {
		return fmt.Errorf("journal job phase: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return domain.E(domain.CodeConflict, "job is no longer running")
	}
	j, err := s.Get(ctx, e.Job.ID)
	if err != nil {
		return err
	}
	s.publish(j)
	return nil
}

var credentials = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|authorization|cookie|credential)["']?\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
var bearer = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)
var userinfo = regexp.MustCompile(`://[^/@\s]+:[^/@\s]+@`)
var query = regexp.MustCompile(`https?://[^\s?]+\?[^\s]+`)

func redact(message string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = credentials.ReplaceAllString(message, "$1=[redacted]")
	message = bearer.ReplaceAllString(message, "Bearer [redacted]")
	message = userinfo.ReplaceAllString(message, "://[redacted]@")
	message = query.ReplaceAllStringFunc(message, func(v string) string { return strings.SplitN(v, "?", 2)[0] + "?[redacted]" })
	return message
}
func payloadSecrets(payload []byte) []string {
	var value any
	_ = json.Unmarshal(payload, &value)
	var out []string
	var walk func(any, bool)
	walk = func(v any, sensitive bool) {
		switch v := v.(type) {
		case map[string]any:
			for k, item := range v {
				key := strings.ToLower(k)
				hidden := sensitive
				for _, word := range []string{"password", "passwd", "secret", "token", "key", "authorization", "cookie", "credential"} {
					if strings.Contains(key, word) {
						hidden = true
					}
				}
				walk(item, hidden)
			}
		case []any:
			for _, item := range v {
				walk(item, sensitive)
			}
		case string:
			if sensitive && v != "" {
				out = append(out, v)
			}
		}
	}
	walk(value, false)
	return out
}

// Log accepts application-authored safe diagnostic text, never raw provider
// response bodies, commands with credentials, or arbitrary configuration. The
// redactor is defense in depth; source errors are always replaced by safe text.
func (e *Execution) Log(ctx context.Context, message string) error {
	s := e.service
	message = redact(message, e.secrets)
	if len(message) > 8192 {
		message = message[:8192] + " [truncated]"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.Get(ctx, e.Job.ID)
	if err != nil {
		return err
	}
	if j.State != domain.JobRunning {
		return domain.E(domain.CodeConflict, "job is no longer running")
	}
	ts := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO job_logs(job_id,created_at,message) SELECT ?,?,? WHERE (SELECT COUNT(*) FROM job_logs WHERE job_id=?) < 10000`, e.Job.ID, ts.Format(time.RFC3339Nano), message, e.Job.ID)
	if err != nil {
		return fmt.Errorf("persist job output: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.E(domain.CodeConflict, "job log limit reached")
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	line := events.Log{ID: id, JobID: e.Job.ID, CreatedAt: ts, Message: message}
	s.hub.Publish(events.Event{Type: "log", JobID: e.Job.ID, Log: &line})
	return nil
}
