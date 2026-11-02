// Package notify is how the conductor tells a person something outside any conversation:
// a Slack direct message, or an email.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	agentslack "github.com/podium-ade/podium/internal/agent/slack"
)

// Slack sends a direct message from the bot. current is the Slack source now; nil when Slack is
// not connected, which is a failure the next notifier can take over from.
type Slack struct {
	Current func() *agentslack.Source
}

// Name implements conductor.Notifier.
func (Slack) Name() string { return "slack" }

// Notify implements conductor.Notifier.
func (s Slack) Notify(ctx context.Context, login, subject, body string) error {
	src := s.Current()
	if src == nil {
		return errors.New("slack is not connected")
	}
	return src.DirectMessage(ctx, login, "*"+subject+"*\n"+body)
}

// Email sends a plain-text message over SMTP.
type Email struct {
	// URL is smtp://user:password@host:port. The connection is upgraded with STARTTLS when the
	// server offers it, and the credentials are only sent over it.
	URL  string
	From string
	// send is smtp.SendMail; a test replaces it.
	send func(addr string, a smtp.Auth, from string, to []string, msg []byte) error
}

// Name implements conductor.Notifier.
func (Email) Name() string { return "email" }

// Validate checks the URL and the sender before the conductor starts with them.
func (e Email) Validate() error {
	u, err := url.Parse(e.URL)
	if err != nil || u.Scheme != "smtp" || u.Hostname() == "" {
		return fmt.Errorf("PODIUM_AGENT_SMTP_URL is not smtp://user:password@host:port")
	}
	if _, err := mail.ParseAddress(e.From); err != nil {
		return fmt.Errorf("PODIUM_AGENT_SMTP_FROM is not an address: %w", err)
	}
	return nil
}

// Notify implements conductor.Notifier. login is the person's email.
func (e Email) Notify(_ context.Context, login, subject, body string) error {
	to, err := mail.ParseAddress(login)
	if err != nil {
		return fmt.Errorf("%s is not an email address", login)
	}
	u, err := url.Parse(e.URL)
	if err != nil {
		return err
	}
	port := u.Port()
	if port == "" {
		port = "587"
	}
	var auth smtp.Auth
	if u.User != nil {
		password, _ := u.User.Password()
		auth = smtp.PlainAuth("", u.User.Username(), password, u.Hostname())
	}
	msg := strings.Join([]string{
		"From: " + e.From,
		"To: " + to.Address,
		"Subject: " + mimeHeader(subject),
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		strings.ReplaceAll(body, "\n", "\r\n"),
	}, "\r\n")
	send := e.send
	if send == nil {
		send = smtp.SendMail
	}
	return send(net.JoinHostPort(u.Hostname(), port), auth, e.From, []string{to.Address}, []byte(msg))
}

// mimeHeader keeps a header to one line, and encodes it when it is not ASCII.
func mimeHeader(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	for _, r := range s {
		if r > 127 {
			return "=?utf-8?q?" + qEncode(s) + "?="
		}
	}
	return s
}

func qEncode(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		switch {
		case c == ' ':
			b.WriteByte('_')
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "=%02X", c)
		}
	}
	return b.String()
}
