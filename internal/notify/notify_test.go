package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"github.com/jonasthim/minecraft-server-ui/internal/db"
	"github.com/jonasthim/minecraft-server-ui/internal/domain"
	"github.com/jonasthim/minecraft-server-ui/internal/jobs"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, *time.Time, domain.User) {
	t.Helper()
	database, e := db.OpenMemory(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { database.Close() })
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	_, e = database.Exec(`INSERT INTO users(id,username,role,created_at,updated_at)VALUES(1,'admin','admin','now','now');INSERT INTO instances(id,name,flavor,minecraft_version,java_major,memory_mib,game_port)VALUES('alpha','Alpha','vanilla','1.21.9',21,1024,25565)`)
	if e != nil {
		t.Fatal(e)
	}
	return New(Config{DB: database, Now: func() time.Time { return now }}), &now, domain.User{ID: 1, Username: "admin", Role: domain.RoleAdmin}
}
func input(endpoint string) ChannelInput {
	return ChannelInput{Label: "Operations", Provider: Webhook, Events: []EventKind{JobFailed, JobSucceeded}, Replace: &Destination{Endpoint: endpoint, Secret: "top-secret-query-token"}}
}
func enable(t *testing.T, s *Service, actor domain.User, in ChannelInput) {
	t.Helper()
	if _, e := s.Save(t.Context(), "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	in.Replace = nil
	in.Enabled = true
	if _, e := s.Save(t.Context(), "ops", in, actor); e != nil {
		t.Fatal(e)
	}
}
func event(now time.Time, id string) Event {
	return Event{ID: id, InstanceID: "alpha", Kind: JobFailed, Operation: "backup_create", CreatedAt: now}
}
func TestMaskedConfigurationAtomicAuditAndClear(t *testing.T) {
	s, _, actor := fixture(t)
	ctx := t.Context()
	in := input("https://example.org/hook?token=url-secret")
	in.Enabled = true
	if _, e := s.Save(ctx, "ops", in, actor); domain.AsError(e).Code != domain.CodeValidationFailed {
		t.Fatal("new channel enabled without disabled save", e)
	}
	in.Enabled = false
	for _, role := range []domain.Role{domain.RoleViewer, domain.RoleOperator} {
		u := actor
		u.Role = role
		if _, e := s.Save(ctx, "ops", in, u); domain.AsError(e).Code != domain.CodeForbidden {
			t.Fatal(role, e)
		}
	}
	enable(t, s, actor, in)
	channels, e := s.List(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(channels)
	public := string(b)
	for _, secret := range []string{"url-secret", "example.org", "top-secret", "recipient"} {
		if secret != "recipient" && strings.Contains(public, secret) {
			t.Fatal("secret leaked", public)
		}
	}
	if !channels[0].Configured || !channels[0].SecretSet || !channels[0].Enabled {
		t.Fatal(channels)
	}
	var private string
	if e = s.cfg.DB.QueryRow(`SELECT private_json FROM notification_channels`).Scan(&private); e != nil || !strings.Contains(private, "url-secret") {
		t.Fatal("secret not stored privately", e)
	}
	rows, e := s.cfg.DB.Query(`SELECT details_json FROM audit_log WHERE action LIKE 'notifications.%'`)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var detail string
		if e = rows.Scan(&detail); e != nil {
			t.Fatal(e)
		}
		if detail != "{}" {
			t.Fatal("audit contains configuration", detail)
		}
	}
	rows.Close()
	in.Replace = nil
	in.Enabled = true
	in.Clear = true
	c, e := s.Save(ctx, "ops", in, actor)
	if e != nil || c.Enabled || c.Configured || c.SecretSet {
		t.Fatal(c, e)
	}
	if e = s.cfg.DB.QueryRow(`SELECT private_json FROM notification_channels`).Scan(&private); e != nil || strings.Contains(private, "secret-query") || strings.Contains(private, "example.org") {
		t.Fatal("clear retained secret", private, e)
	}
}
func TestProviderPayloadsAtControlledTLSReceivers(t *testing.T) {
	for _, provider := range []Provider{Discord, Slack, Ntfy, Telegram, Webhook} {
		t.Run(string(provider), func(t *testing.T) {
			var calls atomic.Int32
			receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
				}
				if r.Method != "POST" {
					t.Error(r.Method)
				}
				switch provider {
				case Discord:
					var p struct {
						Content string `json:"content"`
						Allowed struct {
							Parse []string `json:"parse"`
						} `json:"allowed_mentions"`
					}
					if json.Unmarshal(body, &p) != nil || p.Content == "" || p.Allowed.Parse == nil || len(p.Allowed.Parse) != 0 || r.URL.Query().Get("wait") != "true" {
						t.Error(string(body), r.URL.RawQuery)
					}
					w.WriteHeader(200)
					_, _ = io.WriteString(w, `{"id":"1"}`)
				case Slack:
					var p map[string]any
					if json.Unmarshal(body, &p) != nil || p["mrkdwn"] != false || p["link_names"] != false || !strings.Contains(string(body), "plain_text") {
						t.Error(string(body))
					}
					_, _ = io.WriteString(w, "ok")
				case Ntfy:
					if r.Header.Get("Authorization") != "Bearer private-token" || !strings.Contains(string(body), "backup_create failed") || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
						t.Error(string(body), r.Header)
					}
					_, _ = io.WriteString(w, `{"event":"message"}`)
				case Telegram:
					var p map[string]any
					if json.Unmarshal(body, &p) != nil || p["chat_id"] != "private-chat" || p["parse_mode"] != nil {
						t.Error(string(body))
					}
					_, _ = io.WriteString(w, `{"ok":true}`)
				case Webhook:
					var e Event
					if json.Unmarshal(body, &e) != nil || e.Kind != JobFailed || e.InstanceID != "alpha" {
						t.Error(string(body))
					}
					w.WriteHeader(204)
				}
			}))
			defer receiver.Close()
			c := privateChannel{Channel: Channel{Provider: provider}, Destination: Destination{Endpoint: receiver.URL, Secret: "private-token", Recipient: "private-chat"}}
			if e := sendHTTP(t.Context(), receiver.Client(), c, event(time.Now(), "event-1")); e != nil {
				t.Fatal(e)
			}
			if calls.Load() != 1 {
				t.Fatal(calls.Load())
			}
		})
	}
}
func TestProviderRedirectMalformedTimeoutTLSAndSafeFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider Provider
		status   int
		body     string
		want     string
	}{{"redirect", Webhook, 302, "location-secret", "redirect_rejected"}, {"auth", Ntfy, 403, "private-token", "authentication_rejected"}, {"ratelimit", Discord, 429, "channel-secret", "rate_limited"}, {"telegram", Telegram, 200, `{"ok":false,"description":"private"}`, "provider_rejected"}, {"malformed", Telegram, 200, "invalid", "provider_rejected"}, {"slack", Slack, 200, "private-token", "provider_rejected"}, {"oversized", Webhook, 200, strings.Repeat("S", 16385), "response_invalid"}} {
		t.Run(test.name, func(t *testing.T) {
			receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://127.0.0.1/private-secret")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer receiver.Close()
			client := receiver.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			c := privateChannel{Channel: Channel{Provider: test.provider}, Destination: Destination{Endpoint: receiver.URL}}
			e := sendHTTP(t.Context(), client, c, event(time.Now(), "e"))
			if e == nil || e.Error() != test.want {
				t.Fatal(e)
			}
		})
	}
	release := make(chan struct{})
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer receiver.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	c := privateChannel{Channel: Channel{Provider: Webhook}, Destination: Destination{Endpoint: receiver.URL + "?token=hidden"}}
	if e := sendHTTP(ctx, receiver.Client(), c, event(time.Now(), "e")); e == nil || e.Error() != "timeout" {
		t.Fatal(e)
	}
	c.AllowPrivate = true
	if e := send(t.Context(), c, event(time.Now(), "e")); e == nil || e.Error() != "transport_failed" {
		t.Fatal("untrusted TLS accepted", e)
	}
	c.AllowPrivate = false
	if e := send(t.Context(), c, event(time.Now(), "e")); e == nil {
		t.Fatal("loopback accepted without admin scope")
	}
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "0.0.0.0", "224.0.0.1"} {
		if allowedIP(net.ParseIP(ip), false) {
			t.Fatal("private/special IP accepted", ip)
		}
	}
	if allowedIP(net.ParseIP("169.254.169.254"), true) {
		t.Fatal("metadata scope accepted")
	}
}
func TestDurableLateTerminalOutboxRetryCrashAndNoSecretHistory(t *testing.T) {
	s, now, actor := fixture(t)
	enable(t, s, actor, input("https://example.org/hook?secret=never-echo"))
	ctx := t.Context()
	// Finish newer job first, then old job: a rowid checkpoint would miss old.
	for _, id := range []string{"old", "new"} {
		if _, e := s.cfg.DB.Exec(`INSERT INTO jobs(id,instance_id,kind,state,created_at)VALUES(?,'alpha','backup_create','queued','now')`, id); e != nil {
			t.Fatal(e)
		}
	}
	for _, id := range []string{"new", "old"} {
		if _, e := s.cfg.DB.Exec(`UPDATE jobs SET state='failed',error='private-secret' WHERE id=?`, id); e != nil {
			t.Fatal(e)
		}
	}
	// Trigger event observation timestamps are DB wall time, while fake clock is
	// chosen for deterministic retry arithmetic. Both event IDs remain durable.
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	h, e := s.History(ctx, 0, 100)
	if e != nil || len(h.Deliveries) != 2 {
		t.Fatal(h, e)
	}
	attempts := 0
	s.transport = func(context.Context, privateChannel, Event) error {
		attempts++
		return safeFailure("provider_rejected")
	}
	if sent, e := s.deliverOne(ctx); e != nil || !sent {
		t.Fatal(sent, e)
	}
	h, _ = s.History(ctx, 0, 100)
	if h.Deliveries[1].Attempts != 1 || h.Deliveries[1].Status != "queued" {
		t.Fatal(h)
	}
	// Other event can proceed while first is in backoff; no flood on restart.
	if _, e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	if sent, e := s.deliverOne(ctx); e != nil || sent {
		t.Fatal("backoff bypassed", sent, e)
	}
	*now = now.Add(30 * time.Second)
	if _, e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	*now = now.Add(2 * time.Minute)
	if _, e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.deliverOne(ctx); e != nil {
		t.Fatal(e)
	}
	if attempts != 6 {
		t.Fatal(attempts)
	}
	h, _ = s.History(ctx, 0, 100)
	for _, d := range h.Deliveries {
		if d.Attempts != 3 || d.Status != "failed" {
			t.Fatal(d)
		}
	}
	if _, e = s.cfg.DB.Exec(`UPDATE notification_deliveries SET status='sending' WHERE id=?`, h.Deliveries[0].ID); e != nil {
		t.Fatal(e)
	}
	if e = s.recover(ctx); e != nil {
		t.Fatal(e)
	}
	h, _ = s.History(ctx, 0, 100)
	if h.Deliveries[0].Status != "unknown_after_crash" {
		t.Fatal(h)
	}
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), "private-secret") || strings.Contains(string(b), "never-echo") || strings.Contains(string(b), "example.org") {
		t.Fatal(string(b))
	}
}
func TestDisabledSelectionConfigurationChangeQueueLimitRetentionAndDrain(t *testing.T) {
	s, now, actor := fixture(t)
	ctx := t.Context()
	in := input("https://example.org/hook")
	if _, e := s.Save(ctx, "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	if e := s.Enqueue(ctx, event(*now, "disabled")); e != nil {
		t.Fatal(e)
	}
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	h, _ := s.History(ctx, 0, 100)
	if len(h.Deliveries) != 0 {
		t.Fatal("disabled channel sent")
	}
	// Same-clock events queued while disabled must not cross the explicit
	// enable boundary merely because wall timestamps have equal precision.
	if e := s.Enqueue(ctx, event(*now, "pending-before-enable")); e != nil {
		t.Fatal(e)
	}
	in.Replace = nil
	in.Enabled = true
	if _, e := s.Save(ctx, "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	if e := s.Enqueue(ctx, event(*now, "enabled")); e != nil {
		t.Fatal(e)
	}
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	historyAfterEnable, _ := s.History(ctx, 0, 100)
	if len(historyAfterEnable.Deliveries) != 1 {
		t.Fatal("pre-enable event crossed configuration baseline", historyAfterEnable)
	}
	in.Clear = true
	if _, e := s.Save(ctx, "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	h, _ = s.History(ctx, 0, 100)
	if h.Deliveries[0].Status != "failed" || h.Deliveries[0].SafeFailure != "configuration_changed" {
		t.Fatal(h)
	}
	cutoff := now.Add(-HistoryRetention).Unix()
	for _, created := range []int64{cutoff - 1, cutoff} {
		if _, e := s.cfg.DB.Exec(`INSERT INTO notification_deliveries(channel_id,event_id,provider,instance_id,event_kind,status,next_attempt,created_at,updated_at)VALUES('ops',?,'webhook','alpha','job_failed','failed',0,?,?)`, strconv.FormatInt(created, 10), created, created); e != nil {
			t.Fatal(e)
		}
	}
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.cfg.DB.QueryRow(`SELECT COUNT(*)FROM notification_deliveries WHERE created_at BETWEEN ? AND ?`, cutoff-1, cutoff).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if e := s.Run(canceled); e != nil && e != context.Canceled {
		t.Fatal(e)
	}
}
func TestSMTPRequiresTLSAndAuthenticatedControlledDelivery(t *testing.T) {
	// The TLS fixture provides a trust root only to an unexported transport helper;
	// production configuration cannot disable certificate verification.
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	cert := certServer.Certificate()
	tlsCfg := certServer.TLS.Clone()
	certServer.Close()
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	listener, e := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	captured := make(chan string, 1)
	serverError := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			serverError <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 local test\r\n")
		var message strings.Builder
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				serverError <- e
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(conn, "250-local\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				_, _ = io.WriteString(conn, "235 authenticated\r\n")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				_, _ = io.WriteString(conn, "250 ok\r\n")
			case strings.HasPrefix(line, "DATA"):
				_, _ = io.WriteString(conn, "354 continue\r\n")
				for {
					part, e := reader.ReadString('\n')
					if e != nil {
						serverError <- e
						return
					}
					if part == ".\r\n" {
						break
					}
					message.WriteString(part)
				}
				_, _ = io.WriteString(conn, "250 accepted\r\n")
			case strings.HasPrefix(line, "QUIT"):
				_, _ = io.WriteString(conn, "221 bye\r\n")
				captured <- message.String()
				return
			default:
				serverError <- fmt.Errorf("unexpected SMTP command")
				return
			}
		}
	}()
	u, _ := url.Parse("https://" + listener.Addr().String())
	port, _ := strconv.Atoi(u.Port())
	c := privateChannel{Channel: Channel{Provider: Email, AllowPrivate: true}, Destination: Destination{SMTPHost: u.Hostname(), SMTPPort: port, SMTPUser: "sender@example.org", SMTPPassword: "email-secret", Recipient: "person@example.org", From: "sender@example.org", TLSMode: "tls"}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ev := event(time.Now(), "email")
	if e = sendEmailTLS(ctx, c, &ev, &tls.Config{RootCAs: roots}); e != nil {
		t.Fatal(e)
	}
	select {
	case body := <-captured:
		if !strings.Contains(body, "backup_create failed") || strings.Contains(body, "email-secret") {
			t.Fatal(body)
		}
	case e := <-serverError:
		t.Fatal(e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	plain, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer plain.Close()
	go func() {
		conn, e := plain.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 test\r\n")
		_, _ = reader.ReadString('\n')
		_, _ = io.WriteString(conn, "250 no-tls\r\n")
		_, _ = reader.ReadString('\n')
	}()
	_, portText, _ := net.SplitHostPort(plain.Addr().String())
	c.Destination.SMTPPort, _ = strconv.Atoi(portText)
	c.Destination.TLSMode = "starttls"
	if e = sendEmail(ctx, c, &ev); e == nil || e.Error() != "tls_required" {
		t.Fatal("SMTP downgrade accepted", e)
	}
	c.Destination.TLSMode = "none"
	if e = validateDestination(Email, c.Destination); e == nil {
		t.Fatal("plaintext SMTP configured")
	}
}

func TestQueueAdmissionBoundAndTransportShutdownDrain(t *testing.T) {
	s, now, actor := fixture(t)
	enable(t, s, actor, input("https://example.org/hook"))
	ctx := t.Context()
	for n := 0; n < 1001; n++ {
		if e := s.Enqueue(ctx, event(*now, fmt.Sprintf("event-%04d", n))); e != nil {
			t.Fatal(e)
		}
	}
	for n := 0; n < 11; n++ {
		if e := s.processOutbox(ctx); e != nil {
			t.Fatal(e)
		}
	}
	var queued, rejected int
	if e := s.cfg.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE status='queued'`).Scan(&queued); e != nil {
		t.Fatal(e)
	}
	if e := s.cfg.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE safe_failure='queue_full'`).Scan(&rejected); e != nil {
		t.Fatal(e)
	}
	if queued != 1000 || rejected != 1 {
		t.Fatal("queue limit ineffective", queued, rejected)
	}
	entered := make(chan struct{})
	s.transport = func(ctx context.Context, _ privateChannel, _ Event) error {
		close(entered)
		<-ctx.Done()
		return safeFailure("timeout")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker did not enter")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("transport did not drain")
	}
	var status string
	if e := s.cfg.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE attempts=1`).Scan(&status); e != nil || status != "unknown_after_crash" {
		t.Fatal("ambiguous shutdown outcome misreported", status, e)
	}
	if e := s.cfg.DB.PingContext(ctx); e != nil {
		t.Fatal("service closed shared database", e)
	}
}

func TestRealDurableJobTerminalTransitionReachesControlledReceiver(t *testing.T) {
	s, _, actor := fixture(t)
	ctx := t.Context()
	received := make(chan Event, 1)
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event Event
		if e := json.NewDecoder(r.Body).Decode(&event); e != nil {
			t.Error(e)
		}
		received <- event
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	in := input(receiver.URL)
	in.AllowPrivate = true
	enable(t, s, actor, in)
	// The real jobs queue owns the terminal state transition. Only the feature
	// handler is a deterministic failure fixture; no test mutates jobs.state here.
	queue := jobs.New(s.cfg.DB, nil)
	if e := queue.Register("backup_create", func(context.Context, *jobs.Execution) error {
		return domain.E(domain.CodeUnavailable, "private-local-path-secret")
	}); e != nil {
		t.Fatal(e)
	}
	if e := queue.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if e := queue.Stop(stop); e != nil {
			t.Error(e)
		}
	}()
	job, e := queue.Enqueue(ctx, jobs.Request{InstanceID: "alpha", Kind: "backup_create", RequestedBy: actor.ID})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		actual, e := queue.Get(ctx, job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if actual.State == domain.JobFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("durable job did not reach terminal state")
		}
		time.Sleep(time.Millisecond)
	}
	s.transport = func(ctx context.Context, c privateChannel, event Event) error {
		return sendHTTP(ctx, receiver.Client(), c, event)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(runCtx) }()
	select {
	case event := <-received:
		if event.ID != job.ID+":failed" || event.Kind != JobFailed || event.Operation != "backup_create" {
			cancel()
			t.Fatal(event)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("real terminal transition did not reach notification provider")
	}
	for {
		history, e := s.History(ctx, 0, 100)
		if e != nil {
			cancel()
			t.Fatal(e)
		}
		if len(history.Deliveries) == 1 && history.Deliveries[0].Status == "sent" {
			b, _ := json.Marshal(history)
			if strings.Contains(string(b), "private-local-path-secret") {
				cancel()
				t.Fatal("raw feature failure leaked")
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("delivery success not durably recorded")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestOutboxSequenceSurvivesRetentionEmptyingTable(t *testing.T) {
	s, now, actor := fixture(t)
	ctx := t.Context()
	in := input("https://example.org/hook")
	if _, e := s.Save(ctx, "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	if e := s.Enqueue(ctx, event(*now, "before-enable")); e != nil {
		t.Fatal(e)
	}
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	in.Replace = nil
	in.Enabled = true
	if _, e := s.Save(ctx, "ops", in, actor); e != nil {
		t.Fatal(e)
	}
	var floor int64
	if e := s.cfg.DB.QueryRow(`SELECT event_floor FROM notification_channels WHERE id='ops'`).Scan(&floor); e != nil || floor == 0 {
		t.Fatal(floor, e)
	}
	*now = now.Add(HistoryRetention + time.Second)
	if e := s.Retain(ctx); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.cfg.DB.QueryRow(`SELECT COUNT(*) FROM notification_events`).Scan(&count); e != nil || count != 0 {
		t.Fatal("retention did not empty outbox", count, e)
	}
	if e := s.Enqueue(ctx, event(*now, "after-retention")); e != nil {
		t.Fatal(e)
	}
	var sequence int64
	if e := s.cfg.DB.QueryRow(`SELECT seq FROM notification_events`).Scan(&sequence); e != nil || sequence <= floor {
		t.Fatal("outbox reused a baseline sequence", sequence, floor, e)
	}
	if e := s.processOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	history, e := s.History(ctx, 0, 100)
	if e != nil || len(history.Deliveries) != 1 || history.Deliveries[0].Status != "queued" {
		t.Fatal("new event suppressed behind retained baseline", history, e)
	}
}
