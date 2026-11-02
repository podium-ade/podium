package notify

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnEmailGoesToThePersonFromTheConfiguredSender(t *testing.T) {
	var addr, from string
	var to []string
	var msg []byte
	var authed bool
	e := Email{URL: "smtp://bot:secret@mail.acme.com", From: "Podium <podium@acme.com>",
		send: func(a string, auth smtp.Auth, f string, t []string, m []byte) error {
			addr, from, to, msg, authed = a, f, t, m, auth != nil
			return nil
		}}
	require.NoError(t, e.Validate())
	require.NoError(t, e.Notify(context.Background(), "ada@acme.com", "Your coder task finished — ✓", "It is green.\nSee the chat."))
	assert.Equal(t, "mail.acme.com:587", addr, "submission port when the URL names none")
	assert.Equal(t, "Podium <podium@acme.com>", from)
	assert.Equal(t, []string{"ada@acme.com"}, to)
	assert.True(t, authed)
	s := string(msg)
	assert.Contains(t, s, "To: ada@acme.com\r\n")
	assert.Contains(t, s, "Subject: =?utf-8?q?", "a subject that is not ASCII is encoded")
	assert.True(t, strings.HasSuffix(s, "It is green.\r\nSee the chat."))
}

func TestAnEmailIsOnlySentToAnEmailAddress(t *testing.T) {
	e := Email{URL: "smtp://mail.acme.com:25", From: "podium@acme.com",
		send: func(string, smtp.Auth, string, []string, []byte) error { t.Fatal("sent"); return nil }}
	assert.Error(t, e.Notify(context.Background(), "not-an-address", "s", "b"))
}

func TestTheSMTPSettingsAreChecked(t *testing.T) {
	assert.Error(t, Email{URL: "http://mail.acme.com", From: "podium@acme.com"}.Validate())
	assert.Error(t, Email{URL: "smtp://mail.acme.com", From: "nobody"}.Validate())
	assert.Equal(t, "plain subject", mimeHeader("plain\nsubject"), "a header stays on one line")
}

// smtpServer is a minimal SMTP server on loopback: enough of RFC 5321 for net/smtp to deliver
// one message, which it records.
func smtpServer(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		say("220 test ESMTP")
		var transcript strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			transcript.WriteString(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 test")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				say("250 ok")
			case cmd == "DATA":
				say("354 go ahead")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					transcript.WriteString(l)
				}
				say("250 queued")
			case cmd == "QUIT":
				say("221 bye")
				got <- transcript.String()
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

// TestAnEmailIsDeliveredOverSMTP: the real net/smtp exchange, against a server on loopback.
func TestAnEmailIsDeliveredOverSMTP(t *testing.T) {
	addr, got := smtpServer(t)
	e := Email{URL: "smtp://" + addr, From: "podium@acme.com"}
	require.NoError(t, e.Validate())
	require.NoError(t, e.Notify(context.Background(), "ada@acme.com", "Your coder task finished", "It is green."))
	transcript := <-got
	assert.Contains(t, transcript, "MAIL FROM:<podium@acme.com>")
	assert.Contains(t, transcript, "RCPT TO:<ada@acme.com>")
	assert.Contains(t, transcript, "Subject: Your coder task finished")
	assert.Contains(t, transcript, "It is green.")
}
