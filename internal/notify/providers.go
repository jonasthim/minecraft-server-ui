package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// safeFailure contains only an internal enum. Provider bodies, URLs, SMTP
// responses and wrapped net/http errors are intentionally discarded.
type safeFailure string

func (e safeFailure) Error() string { return string(e) }
func eventText(event Event) string {
	if event.Kind == ServerCrashed {
		return "Minecraft server " + event.InstanceID + ": unexpected server exit."
	}
	outcome := "succeeded"
	if event.Kind == JobFailed {
		outcome = "failed"
	}
	// Every token is validated against finite operation/kind and instance grammars
	// before persistence. There is no user-controlled template/code/raw job output.
	return "Minecraft server " + event.InstanceID + ": " + event.Operation + " " + outcome + "."
}
func providerPayload(c privateChannel, e Event) ([]byte, string, error) {
	text := eventText(e)
	var payload any
	switch c.Provider {
	case Discord:
		payload = map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}
	case Slack:
		payload = map[string]any{"text": text, "mrkdwn": false, "link_names": false, "blocks": []any{map[string]any{"type": "section", "text": map[string]string{"type": "plain_text", "text": text}}}}
	case Ntfy:
		return []byte(text), "text/plain; charset=utf-8", nil
	case Telegram:
		payload = map[string]any{"chat_id": c.Destination.Recipient, "text": text, "link_preview_options": map[string]bool{"is_disabled": true}}
	case Webhook:
		payload = e
	default:
		return nil, "", safeFailure("invalid_provider")
	}
	b, err := json.Marshal(payload)
	return b, "application/json", err
}
func secureDial(ctx context.Context, network, addr string, allowPrivate bool) (net.Conn, error) {
	host, port, e := net.SplitHostPort(addr)
	if e != nil {
		return nil, safeFailure("destination_rejected")
	}
	addresses, e := net.DefaultResolver.LookupIPAddr(ctx, host)
	if e != nil || len(addresses) == 0 {
		return nil, safeFailure("dns_failed")
	}
	// Reject mixed public/private answers before dialing; pin the IP to close DNS
	// rebinding between validation and connect. Proxy env settings are never used.
	for _, address := range addresses {
		if !allowedIP(address.IP, allowPrivate) {
			return nil, safeFailure("destination_rejected")
		}
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	for _, address := range addresses {
		conn, e := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if e == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, safeFailure("timeout")
		}
	}
	return nil, safeFailure("connection_failed")
}
func send(ctx context.Context, c privateChannel, e Event) error {
	if err := validateDestination(c.Provider, c.Destination); err != nil {
		return safeFailure("configuration_invalid")
	}
	if c.Provider == Email {
		return sendEmail(ctx, c, &e)
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		return secureDial(ctx, network, addr, c.AllowPrivate)
	}, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16384, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: DeliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return sendHTTP(ctx, client, c, e)
}
func sendHTTP(ctx context.Context, client *http.Client, c privateChannel, e Event) error {
	payload, content, e2 := providerPayload(c, e)
	if e2 != nil {
		return safeFailure("payload_invalid")
	}
	endpoint := c.Destination.Endpoint
	if c.Provider == Discord {
		u, err := url.Parse(endpoint)
		if err != nil {
			return safeFailure("configuration_invalid")
		}
		q := u.Query()
		q.Set("wait", "true")
		u.RawQuery = q.Encode()
		endpoint = u.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return safeFailure("configuration_invalid")
	}
	req.Header.Set("Content-Type", content)
	req.Header.Set("User-Agent", "minecraft-server-ui/notifications")
	if c.Destination.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Destination.Secret)
	}
	if c.Provider == Ntfy {
		req.Header.Set("Title", "Minecraft server event")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return safeFailure("timeout")
		}
		return safeFailure("transport_failed")
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return safeFailure("redirect_rejected")
	}
	if response.StatusCode == 429 {
		return safeFailure("rate_limited")
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return safeFailure("authentication_rejected")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return safeFailure("provider_rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(body) > 16384 {
		return safeFailure("response_invalid")
	}
	if c.Provider == Telegram {
		var result struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(body, &result) != nil || !result.OK {
			return safeFailure("provider_rejected")
		}
	}
	if c.Provider == Slack && strings.TrimSpace(string(body)) != "ok" {
		return safeFailure("provider_rejected")
	}
	return nil
}
func sendEmail(ctx context.Context, c privateChannel, event *Event) error {
	return sendEmailTLS(ctx, c, event, nil)
}
func sendEmailTLS(ctx context.Context, c privateChannel, event *Event, trusted *tls.Config) error {
	d := c.Destination
	address := net.JoinHostPort(d.SMTPHost, strconv.Itoa(d.SMTPPort))
	conn, err := secureDial(ctx, "tcp", address, c.AllowPrivate)
	if err != nil {
		return safeFailure("connection_failed")
	}
	defer conn.Close()
	rawConnection := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConnection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(DeliveryTimeout))
	}
	tlsConfig := &tls.Config{ServerName: d.SMTPHost, MinVersion: tls.VersionTLS12}
	if trusted != nil {
		tlsConfig = trusted.Clone()
		tlsConfig.ServerName = d.SMTPHost
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if d.TLSMode == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			return safeFailure("tls_failed")
		}
		conn = secure
	}
	return smtpExchange(ctx, conn, c, event, tlsConfig)
}
