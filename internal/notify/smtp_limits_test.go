package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMTPBoundsPlainAndDecryptedResponses(t *testing.T) {
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	config := certServer.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certServer.Certificate())
	certServer.Close()
	for _, test := range []struct {
		name, mode, payload string
		afterTLS            bool
	}{{"single-line-greeting", "starttls", "220 " + strings.Repeat("s", 1<<20) + "\r\n", false}, {"multiline-greeting", "starttls", strings.Repeat("220-"+strings.Repeat("s", 1000)+"\r\n", 2000) + "220 end\r\n", false}, {"post-tls-ehlo", "tls", strings.Repeat("250-"+strings.Repeat("s", 1000)+"\r\n", 2000) + "250 end\r\n", true}} {
		t.Run(test.name, func(t *testing.T) {
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			if test.afterTLS {
				listener = tls.NewListener(listener, config)
			}
			defer listener.Close()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				if test.afterTLS {
					_, _ = io.WriteString(conn, "220 test\r\n")
					_, _ = bufio.NewReader(conn).ReadString('\n')
				}
				_, _ = io.WriteString(conn, test.payload)
			}()
			host, portText, _ := net.SplitHostPort(listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			c := privateChannel{Channel: Channel{Provider: Email, AllowPrivate: true}, Destination: Destination{SMTPHost: host, SMTPPort: port, TLSMode: test.mode, From: "from@example.org", Recipient: "to@example.org"}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			e = sendEmailTLS(ctx, c, nil, &tls.Config{RootCAs: roots})
			if e == nil || e.Error() != "smtp_response_limit" {
				t.Fatalf("unbounded SMTP response not rejected safely: %v", e)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("bounded SMTP client did not close oversized peer")
			}
		})
	}
}

func TestSTARTTLSBoundsSMTPPlaintextWithoutCountingTLSRecords(t *testing.T) {
	certServer := httptest.NewTLSServer(http.NotFoundHandler())
	config := certServer.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(certServer.Certificate())
	certServer.Close()
	// A large certificate flight is legitimate TLS traffic, not SMTP response
	// text. Its bytes exceed the entire SMTP plaintext response budget.
	leaf := config.Certificates[0].Certificate[0]
	for n := 0; n < 90; n++ {
		config.Certificates[0].Certificate = append(config.Certificates[0].Certificate, leaf)
	}
	certificateBytes := 0
	for _, der := range config.Certificates[0].Certificate {
		certificateBytes += len(der)
	}
	if certificateBytes <= smtpResponseBytes {
		t.Fatal("TLS fixture does not exceed plaintext budget", certificateBytes)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	finished := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			finished <- e
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		reader := bufio.NewReader(conn)
		_, _ = io.WriteString(conn, "220 local test\r\n")
		line, e := reader.ReadString('\n')
		if e != nil || !strings.HasPrefix(line, "EHLO") {
			finished <- fmt.Errorf("unexpected pre-TLS command")
			return
		}
		_, _ = io.WriteString(conn, "250-local\r\n250 STARTTLS\r\n")
		line, e = reader.ReadString('\n')
		if e != nil || line != "STARTTLS\r\n" {
			finished <- fmt.Errorf("credentials or other data sent before TLS")
			return
		}
		_, _ = io.WriteString(conn, "220 start TLS\r\n")
		secure := tls.Server(conn, config)
		if e = secure.Handshake(); e != nil {
			finished <- e
			return
		}
		reader = bufio.NewReader(secure)
		for {
			line, e = reader.ReadString('\n')
			if e != nil {
				finished <- e
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				_, _ = io.WriteString(secure, "250-local\r\n250 AUTH PLAIN\r\n")
			case strings.HasPrefix(line, "AUTH PLAIN"):
				_, _ = io.WriteString(secure, "235 ok\r\n")
			case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
				_, _ = io.WriteString(secure, "250 ok\r\n")
			case line == "DATA\r\n":
				_, _ = io.WriteString(secure, "354 data\r\n")
				for {
					line, e = reader.ReadString('\n')
					if e != nil {
						finished <- e
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				_, _ = io.WriteString(secure, "250 accepted\r\n")
			case line == "QUIT\r\n":
				_, _ = io.WriteString(secure, "221 bye\r\n")
				finished <- nil
				return
			default:
				finished <- fmt.Errorf("unexpected SMTP command")
				return
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	c := privateChannel{Channel: Channel{Provider: Email, AllowPrivate: true}, Destination: Destination{SMTPHost: host, SMTPPort: port, TLSMode: "starttls", From: "from@example.org", Recipient: "to@example.org", SMTPUser: "local-user", SMTPPassword: "local-password"}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if e = sendEmailTLS(ctx, c, nil, &tls.Config{RootCAs: roots}); e != nil {
		t.Fatal(e)
	}
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
}
func TestSMTPResponseReadCancellationClosesPeer(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	entered := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		close(entered)
		_, _ = io.Copy(io.Discard, conn)
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	c := privateChannel{Channel: Channel{Provider: Email, AllowPrivate: true}, Destination: Destination{SMTPHost: host, SMTPPort: port, TLSMode: "starttls", From: "from@example.org", Recipient: "to@example.org"}}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- sendEmailTLS(ctx, c, nil, nil) }()
	<-entered
	cancel()
	select {
	case e := <-result:
		if e == nil || e.Error() != "timeout" {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP cancellation did not drain")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("SMTP peer connection remained open")
	}
}
