package mail

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server: no TLS, optionally AUTH PLAIN.
type fakeSMTP struct {
	ln         net.Listener
	offerAuth  bool
	mu         sync.Mutex
	authLine   string
	mailFrom   string
	rcptTo     []string
	data       string
	delivered  chan struct{}
	offerStart bool // advertise STARTTLS (never actually upgrades)
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, delivered: make(chan struct{}, 1)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	write := func(s string) { _, _ = io.WriteString(conn, s+"\r\n") }
	write("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			lines := []string{"250-fake"}
			if f.offerAuth {
				lines = append(lines, "250-AUTH PLAIN")
			}
			if f.offerStart {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 OK")
			for _, l := range lines {
				write(l)
			}
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			f.mu.Lock()
			f.authLine = line
			f.mu.Unlock()
			write("235 2.7.0 ok")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.mailFrom = line
			f.mu.Unlock()
			write("250 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			f.mu.Lock()
			f.rcptTo = append(f.rcptTo, line)
			f.mu.Unlock()
			write("250 OK")
		case upper == "DATA":
			write("354 go ahead")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			write("250 queued")
			select {
			case f.delivered <- struct{}{}:
			default:
			}
		case upper == "QUIT":
			write("221 bye")
			return
		default:
			write("502 not implemented")
		}
	}
}

func newMailer(t *testing.T, f *fakeSMTP, mut func(*SMTPConfig)) *SMTP {
	t.Helper()
	cfg := SMTPConfig{Host: "127.0.0.1", Port: f.port(), From: "Alert Bot <bot@corp.example>", TLS: TLSNone, Timeout: 5 * time.Second}
	if mut != nil {
		mut(&cfg)
	}
	m, err := NewSMTP(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestSMTP_SendsMessage(t *testing.T) {
	f := newFakeSMTP(t)
	m := newMailer(t, f, nil)

	err := m.Send(context.Background(), "Ann <ann@corp.example>", "Your code", "Code: 123456\nBye")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("message not delivered")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.mailFrom, "<bot@corp.example>") {
		t.Errorf("MAIL FROM = %q", f.mailFrom)
	}
	if len(f.rcptTo) != 1 || !strings.Contains(f.rcptTo[0], "<ann@corp.example>") {
		t.Errorf("RCPT TO = %v", f.rcptTo)
	}
	for _, want := range []string{
		"From: bot@corp.example\r\n",
		"To: ann@corp.example\r\n",
		"Subject: Your code\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"\r\n\r\nCode: 123456\r\nBye\r\n",
	} {
		if !strings.Contains(f.data, want) {
			t.Errorf("message misses %q in:\n%s", want, f.data)
		}
	}
}

func TestSMTP_AuthPlain(t *testing.T) {
	f := newFakeSMTP(t)
	f.offerAuth = true
	m := newMailer(t, f, func(c *SMTPConfig) { c.Username, c.Password = "bot", "s3cret" })

	if err := m.Send(context.Background(), "ann@corp.example", "Hi", "x"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	fields := strings.Fields(f.authLine)
	if len(fields) != 3 {
		t.Fatalf("auth line = %q", f.authLine)
	}
	decoded, _ := base64.StdEncoding.DecodeString(fields[2])
	if string(decoded) != "\x00bot\x00s3cret" {
		t.Errorf("credentials = %q", decoded)
	}
}

func TestSMTP_RefusesDowngradeWithoutSTARTTLS(t *testing.T) {
	f := newFakeSMTP(t) // does not advertise STARTTLS
	m := newMailer(t, f, func(c *SMTPConfig) { c.TLS = TLSStartTLS })

	err := m.Send(context.Background(), "ann@corp.example", "Hi", "x")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("want a STARTTLS error, got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.data != "" || len(f.rcptTo) != 0 {
		t.Error("nothing may be sent when STARTTLS is unavailable")
	}
}

func TestSMTP_RejectsBadInput(t *testing.T) {
	f := newFakeSMTP(t)
	m := newMailer(t, f, nil)
	ctx := context.Background()

	if err := m.Send(ctx, "not an address", "Hi", "x"); err == nil {
		t.Error("invalid recipient must be rejected")
	}
	if err := m.Send(ctx, "ann@corp.example", "Hi\r\nBcc: evil@x.test", "x"); err == nil {
		t.Error("header injection in subject must be rejected")
	}
}

func TestSMTP_EncodesNonASCIISubject(t *testing.T) {
	f := newFakeSMTP(t)
	m := newMailer(t, f, nil)
	if err := m.Send(context.Background(), "ann@corp.example", "Dogrulama kodu: şı", "x"); err != nil {
		t.Fatal(err)
	}
	<-f.delivered
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data, "Subject: =?utf-8?q?") {
		t.Errorf("subject should be RFC 2047 encoded:\n%s", f.data)
	}
}

func TestSMTP_ConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	m, err := NewSMTP(SMTPConfig{Host: "127.0.0.1", Port: port, From: "bot@corp.example", TLS: TLSNone, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), "ann@corp.example", "Hi", "x"); err == nil {
		t.Fatal("expected a connection error")
	}
}

func TestNewSMTP_Validation(t *testing.T) {
	bad := []SMTPConfig{
		{Port: 25, From: "bot@corp.example"},
		{Host: "h", From: "bot@corp.example"},
		{Host: "h", Port: 25, From: "nonsense"},
		{Host: "h", Port: 25, From: "bot@corp.example", TLS: "weird"},
	}
	for i, c := range bad {
		if _, err := NewSMTP(c); err == nil {
			t.Errorf("case %d should fail: %+v", i, c)
		}
	}
	m, err := NewSMTP(SMTPConfig{Host: "h", Port: 587, From: "Bot <bot@corp.example>"})
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.TLS != TLSStartTLS || m.cfg.From != "bot@corp.example" || m.cfg.Timeout == 0 {
		t.Errorf("defaults not applied: %+v", m.cfg)
	}
}

func TestLogMailer(t *testing.T) {
	var sb strings.Builder
	log := slog.New(slog.NewTextHandler(&sb, nil))
	if err := (LogMailer{Log: log}).Send(context.Background(), "ann@corp.example", "Code", "123456"); err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if !strings.Contains(out, "ann@corp.example") || !strings.Contains(out, "123456") || !strings.Contains(out, "NOT sent") {
		t.Errorf("log output = %s", out)
	}
}
