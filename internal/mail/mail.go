// Package mail sends plain text emails. LogMailer is for development (it
// prints the message instead of sending it), SMTP is for real delivery.
package mail

import (
	"context"
	"log/slog"
)

// Mailer sends one plain text email.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// LogMailer writes emails to the log instead of sending them. Never use it in
// production: one-time codes end up in the log.
type LogMailer struct {
	Log *slog.Logger
}

// Send implements Mailer.
func (m LogMailer) Send(_ context.Context, to, subject, body string) error {
	m.Log.Warn("MAILER=log: email NOT sent, printing it instead (development only)",
		"to", to, "subject", subject, "body", body)
	return nil
}
