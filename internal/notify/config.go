package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var channelID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

func validProvider(p Provider) bool {
	return p == Discord || p == Slack || p == Ntfy || p == Telegram || p == Webhook || p == Email
}
func validateDestination(p Provider, d Destination) error {
	fail := func() error {
		return domain.E(domain.CodeValidationFailed, "Invalid notification destination; use a secure provider endpoint and bounded credentials.")
	}
	for _, v := range []string{d.Endpoint, d.Secret, d.Recipient, d.SMTPHost, d.SMTPUser, d.SMTPPassword, d.From, d.TLSMode} {
		if len(v) > 2048 || strings.ContainsAny(v, "\r\n\x00") {
			return fail()
		}
	}
	if p == Email {
		if d.Endpoint != "" || d.Secret != "" || d.SMTPHost == "" || strings.ContainsAny(d.SMTPHost, "/:@?# ") || d.SMTPPort < 1 || d.SMTPPort > 65535 || (d.TLSMode != "starttls" && d.TLSMode != "tls") {
			return fail()
		}
		for _, v := range []string{d.Recipient, d.From} {
			a, e := mail.ParseAddress(v)
			if e != nil || a.Address != v {
				return fail()
			}
		}
		if d.SMTPUser == "" && d.SMTPPassword != "" || d.SMTPUser != "" && d.SMTPPassword == "" {
			return fail()
		}
		return nil
	}
	if d.SMTPHost != "" || d.SMTPUser != "" || d.SMTPPassword != "" || d.SMTPPort != 0 || d.From != "" || d.TLSMode != "" {
		return fail()
	}
	u, e := url.Parse(d.Endpoint)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return fail()
	}
	if u.Port() != "" {
		if n, e := strconv.Atoi(u.Port()); e != nil || n < 1 || n > 65535 {
			return fail()
		}
	}
	host := strings.ToLower(u.Hostname())
	switch p {
	case Discord:
		if host != "discord.com" || !strings.HasPrefix(u.Path, "/api/webhooks/") || u.RawQuery != "" {
			return fail()
		}
	case Slack:
		if host != "hooks.slack.com" || !strings.HasPrefix(u.Path, "/services/") || u.RawQuery != "" {
			return fail()
		}
	case Telegram:
		if host != "api.telegram.org" || !regexp.MustCompile(`^/bot[0-9]+:[A-Za-z0-9_-]+/sendMessage$`).MatchString(u.Path) || u.RawQuery != "" || d.Recipient == "" {
			return fail()
		}
	case Ntfy:
		if u.Path == "" || u.Path == "/" || strings.Count(strings.Trim(u.Path, "/"), "/") != 0 || u.RawQuery != "" {
			return fail()
		}
	}
	if p != Telegram && d.Recipient != "" {
		return fail()
	}
	if (p == Discord || p == Slack || p == Telegram) && d.Secret != "" {
		return fail()
	}
	return nil
}
func admin(actor domain.User) error {
	if actor.ID <= 0 || actor.Disabled || actor.Role != domain.RoleAdmin {
		return domain.E(domain.CodeForbidden, "administrator access required")
	}
	return nil
}
func (s *Service) load(ctx context.Context, id string) (privateChannel, error) {
	var c privateChannel
	var events, private string
	e := s.cfg.DB.QueryRowContext(ctx, `SELECT id,label,provider,enabled,events_json,allow_private,private_json FROM notification_channels WHERE id=?`, id).Scan(&c.ID, &c.Label, &c.Provider, &c.Enabled, &events, &c.AllowPrivate, &private)
	if errors.Is(e, sql.ErrNoRows) {
		return c, domain.E(domain.CodeNotFound, "notification channel not found")
	}
	if e != nil {
		return c, e
	}
	if json.Unmarshal([]byte(events), &c.Events) != nil || json.Unmarshal([]byte(private), &c.Destination) != nil {
		return c, fmt.Errorf("invalid private notification configuration")
	}
	c.Configured = c.Destination.Endpoint != "" || c.Destination.SMTPHost != ""
	c.SecretSet = c.Destination.Secret != "" || c.Destination.SMTPPassword != "" || c.Provider == Telegram && c.Configured || c.Provider == Discord && c.Configured || c.Provider == Slack && c.Configured
	c.RecipientSet = c.Destination.Recipient != ""
	return c, nil
}
func (s *Service) List(ctx context.Context) ([]Channel, error) {
	rows, e := s.cfg.DB.QueryContext(ctx, `SELECT id FROM notification_channels ORDER BY id LIMIT 24`)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			break
		}
		ids = append(ids, id)
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return nil, e
	}
	out := []Channel{}
	for _, id := range ids {
		c, e := s.load(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, c.Channel)
	}
	return out, nil
}
func (s *Service) Save(ctx context.Context, id string, in ChannelInput, actor domain.User) (Channel, error) {
	if e := admin(actor); e != nil {
		return Channel{}, e
	}
	if !channelID.MatchString(id) || !validProvider(in.Provider) || len(in.Label) < 1 || len(in.Label) > 80 || strings.ContainsAny(in.Label, "\r\n\x00") || len(in.Events) > 2 || in.Clear && in.Replace != nil {
		return Channel{}, domain.E(domain.CodeValidationFailed, "invalid notification channel")
	}
	seen := map[EventKind]bool{}
	for _, k := range in.Events {
		if k != JobFailed && k != JobSucceeded || seen[k] {
			return Channel{}, domain.E(domain.CodeValidationFailed, "invalid notification event selection")
		}
		seen[k] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old, e := s.load(ctx, id)
	exists := e == nil
	if e != nil && domain.AsError(e).Code != domain.CodeNotFound {
		return Channel{}, e
	}
	if !exists {
		var count int
		if e = s.cfg.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_channels`).Scan(&count); e != nil {
			return Channel{}, e
		}
		if count >= MaxChannels {
			return Channel{}, domain.E(domain.CodeConflict, "notification channel limit reached")
		}
		if in.Enabled {
			return Channel{}, domain.E(domain.CodeValidationFailed, "save a new destination disabled before explicitly enabling delivery")
		}
	}
	d := old.Destination
	if exists && old.Provider != in.Provider && in.Replace == nil && !in.Clear {
		return Channel{}, domain.E(domain.CodeValidationFailed, "provider change requires replacement destination")
	}
	if in.Replace != nil {
		d = *in.Replace
	}
	if in.Clear {
		d = Destination{}
		in.Enabled = false
	}
	if d.Endpoint != "" || d.SMTPHost != "" {
		if e = validateDestination(in.Provider, d); e != nil {
			return Channel{}, e
		}
	} else if in.Enabled {
		return Channel{}, domain.E(domain.CodeValidationFailed, "configure a destination before enabling delivery")
	}
	if in.Events == nil {
		in.Events = []EventKind{}
	}
	events, _ := json.Marshal(in.Events)
	private, _ := json.Marshal(d)
	tx, e := s.cfg.DB.BeginTx(ctx, nil)
	if e != nil {
		return Channel{}, e
	}
	defer tx.Rollback()
	var eventFloor int64
	if e = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM notification_events`).Scan(&eventFloor); e != nil {
		return Channel{}, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO notification_channels(id,label,provider,enabled,events_json,allow_private,private_json,event_floor,updated_at)VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(id)DO UPDATE SET label=excluded.label,provider=excluded.provider,enabled=excluded.enabled,events_json=excluded.events_json,allow_private=excluded.allow_private,private_json=excluded.private_json,event_floor=excluded.event_floor,updated_at=excluded.updated_at`, id, in.Label, in.Provider, in.Enabled, string(events), in.AllowPrivate, string(private), eventFloor, s.cfg.Now().Unix())
	if e != nil {
		return Channel{}, e
	}
	// Replacement/clear/disable invalidates pending work so queued events can never
	// silently reach a newly configured recipient. Sending work is serialized.
	if _, e = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='failed',safe_failure='configuration_changed',updated_at=? WHERE channel_id=? AND status='queued'`, s.cfg.Now().Unix(), id); e != nil {
		return Channel{}, e
	}
	if e = s.audit(ctx, tx, actor, "notifications.configure", id); e != nil {
		return Channel{}, e
	}
	if e = tx.Commit(); e != nil {
		return Channel{}, e
	}
	c, e := s.load(ctx, id)
	return c.Channel, e
}
func (s *Service) Delete(ctx context.Context, id string, actor domain.User) error {
	if e := admin(actor); e != nil {
		return e
	}
	if !channelID.MatchString(id) {
		return domain.E(domain.CodeValidationFailed, "invalid channel id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.cfg.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=?`, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='failed',safe_failure='channel_deleted',updated_at=? WHERE channel_id=? AND status='queued'`, s.cfg.Now().Unix(), id); e != nil {
		return e
	}
	if e = s.audit(ctx, tx, actor, "notifications.delete", id); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Service) audit(ctx context.Context, tx *sql.Tx, actor domain.User, action, id string) error {
	_, e := tx.ExecContext(ctx, `INSERT INTO audit_log(ts,user_id,username,action,target,details_json) VALUES(?,?,?,?,?,'{}')`, s.cfg.Now().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"), actor.ID, actor.Username, action, id)
	return e
}

// Public endpoints are resolved at connection time and pinned to a validated IP.
// Private targets require an explicit administrator choice per channel. Link-local
// metadata and unspecified/multicast targets remain forbidden in all modes.
func allowedIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return allowPrivate || !ip.IsPrivate() && !ip.IsLoopback()
}
