package mail

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// TLSMode says how the connection to the SMTP server is secured.
type TLSMode string

const (
	// TLSStartTLS upgrades a plain connection (usually port 587). A server that
	// does not offer STARTTLS is an error: there is no silent downgrade.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit connects with TLS from the first byte (usually port 465).
	TLSImplicit TLSMode = "tls"
	// TLSNone sends everything unencrypted. Only for local test servers.
	TLSNone TLSMode = "none"
)

// SMTPConfig configures the SMTP mailer.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string // empty: no authentication
	Password string
	From     string // e.g. "alert-bot@corp.example"
	TLS      TLSMode
	Timeout  time.Duration // for the whole delivery; default 30s
}

// SMTP sends mail through an SMTP server using only the standard library.
type SMTP struct {
	cfg SMTPConfig
}

// NewSMTP validates the configuration and returns a mailer.
func NewSMTP(cfg SMTPConfig) (*SMTP, error) {
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, errors.New("smtp: host and port are required")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("smtp: invalid From address: %w", err)
	}
	cfg.From = from.Address
	switch cfg.TLS {
	case "":
		cfg.TLS = TLSStartTLS
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return nil, fmt.Errorf("smtp: unknown TLS mode %q (use starttls, tls or none)", cfg.TLS)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SMTP{cfg: cfg}, nil
}

// Send implements Mailer.
func (m *SMTP) Send(ctx context.Context, to, subject, body string) error {
	rcpt, err := mail.ParseAddress(to)
	if err != nil {
		return fmt.Errorf("smtp: invalid recipient: %w", err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		return errors.New("smtp: subject must be a single line")
	}
	msg := m.buildMessage(rcpt.Address, subject, body)

	deadline := time.Now().Add(m.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	dialer := &net.Dialer{Deadline: deadline}
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))

	var conn net.Conn
	if m.cfg.TLS == TLSImplicit {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("smtp: connect %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer c.Close()

	if m.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not offer STARTTLS (refusing to send unencrypted; set SMTP_TLS=none only for local test servers)")
		}
		if err := c.StartTLS(&tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp: starttls: %w", err)
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth refuses to send the password over an unencrypted connection
		// to anything but localhost.
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("smtp: auth: %w", err)
		}
	}
	if err := c.Mail(m.cfg.From); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("smtp: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: end of data: %w", err)
	}
	return c.Quit()
}

func (m *SMTP) buildMessage(to, subject, body string) []byte {
	var id [8]byte
	_, _ = rand.Read(id[:])
	domain := "localhost"
	if _, d, ok := strings.Cut(m.cfg.From, "@"); ok && d != "" {
		domain = d
	}
	headers := []string{
		"From: " + m.cfg.From,
		"To: " + to,
		"Subject: " + mime.QEncoding.Encode("utf-8", subject),
		"Date: " + time.Now().Format(time.RFC1123Z),
		"Message-ID: <" + hex.EncodeToString(id[:]) + "@" + domain + ">",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 8bit",
	}
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
	return []byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n")
}
