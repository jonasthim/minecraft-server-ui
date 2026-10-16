package notify

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
)

const smtpResponseBytes = 64 << 10
const smtpLineBytes = 4096

var errSMTPResponseLimit = errors.New("SMTP response limit")

type smtpBudget struct {
	remaining, line int
	exceeded        bool
}
type smtpResponseConn struct {
	net.Conn
	budget *smtpBudget
}

func (c *smtpResponseConn) Read(p []byte) (int, error) {
	b := c.budget
	if b.remaining <= 0 || b.line >= smtpLineBytes {
		b.exceeded = true
		return 0, errSMTPResponseLimit
	}
	limit := min(len(p), b.remaining, smtpLineBytes-b.line)
	n, e := c.Conn.Read(p[:limit])
	b.remaining -= n
	for _, ch := range p[:n] {
		if ch == '\n' {
			b.line = 0
		} else {
			b.line++
		}
	}
	return n, e
}

// smtpExchange bounds total plaintext response bytes and individual lines across
// every protocol phase. TLS handshakes/records are read by the underlying raw
// connection, while each textproto reader sees a bounded decrypted connection.
// STARTTLS creates a new reader after the mandatory, verified TLS handshake.
func smtpExchange(ctx context.Context, conn net.Conn, c privateChannel, event *Event, tlsConfig *tls.Config) error {
	budget := &smtpBudget{remaining: smtpResponseBytes}
	text := textproto.NewConn(&smtpResponseConn{conn, budget})
	failure := func(code string) error {
		if budget.exceeded {
			return safeFailure("smtp_response_limit")
		}
		if ctx.Err() != nil {
			return safeFailure("timeout")
		}
		return safeFailure(code)
	}
	command := func(code int, format string, args ...any) (string, error) {
		if e := text.PrintfLine(format, args...); e != nil {
			return "", e
		}
		_, message, e := text.ReadResponse(code)
		return message, e
	}
	if _, _, e := text.ReadResponse(220); e != nil {
		return failure("smtp_failed")
	}
	hello := func() (string, error) { return command(250, "EHLO localhost") }
	extensions, e := hello()
	if e != nil {
		return failure("smtp_failed")
	}
	hasExtension := func(name string) bool {
		for _, line := range strings.Split(extensions, "\n") {
			fields := strings.Fields(line)
			if len(fields) > 0 && strings.EqualFold(fields[0], name) {
				return true
			}
		}
		return false
	}
	if c.Destination.TLSMode == "starttls" {
		if !hasExtension("STARTTLS") {
			return safeFailure("tls_required")
		}
		if _, e = command(220, "STARTTLS"); e != nil {
			return failure("tls_failed")
		}
		secure := tls.Client(conn, tlsConfig)
		if e = secure.HandshakeContext(ctx); e != nil {
			return failure("tls_failed")
		}
		text = textproto.NewConn(&smtpResponseConn{secure, budget})
		extensions, e = hello()
		if e != nil {
			return failure("smtp_failed")
		}
	}
	d := c.Destination
	if d.SMTPUser != "" {
		plain := false
		for _, line := range strings.Split(extensions, "\n") {
			fields := strings.Fields(strings.ToUpper(line))
			if len(fields) > 1 && fields[0] == "AUTH" {
				for _, mechanism := range fields[1:] {
					if mechanism == "PLAIN" {
						plain = true
					}
				}
			}
		}
		if !plain {
			return safeFailure("authentication_rejected")
		}
		credential := base64.StdEncoding.EncodeToString([]byte("\x00" + d.SMTPUser + "\x00" + d.SMTPPassword))
		if e = text.PrintfLine("AUTH PLAIN %s", credential); e != nil {
			return failure("authentication_rejected")
		}
		code, _, authErr := text.ReadResponse(0)
		if authErr == nil && code == 334 {
			_, authErr = command(235, "%s", credential)
		} else if code != 235 {
			authErr = fmt.Errorf("SMTP authentication rejected")
		}
		if authErr != nil {
			return failure("authentication_rejected")
		}
	}
	if _, e = command(250, "MAIL FROM:<%s>", d.From); e != nil {
		return failure("smtp_rejected")
	}
	if _, e = command(25, "RCPT TO:<%s>", d.Recipient); e != nil {
		return failure("smtp_rejected")
	}
	if _, e = command(354, "DATA"); e != nil {
		return failure("smtp_rejected")
	}
	writer := text.DotWriter()
	body := "Minecraft server event."
	if event != nil {
		body = eventText(*event)
	}
	if _, e = fmt.Fprintf(writer, "From: %s\r\nTo: %s\r\nSubject: Minecraft server event\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", d.From, d.Recipient, body); e != nil {
		return failure("smtp_failed")
	}
	if e = writer.Close(); e != nil {
		return failure("smtp_failed")
	}
	if _, _, e = text.ReadResponse(250); e != nil {
		return failure("smtp_failed")
	}
	if _, e = command(221, "QUIT"); e != nil {
		return failure("smtp_failed")
	}
	return nil
}

var _ io.Reader = (*smtpResponseConn)(nil)
