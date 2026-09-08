// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The SMTP client dance shared by the Email drop and the daemon's own mailer.
package smtputil

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// A send needs both the bare address for the envelope and the display form for
// the From: header; a configured sender may carry either.
func SplitSender(from string) (header, envelope string) {
	parsed, err := mail.ParseAddress(from)
	if err != nil {
		return from, from
	}
	if parsed.Name == "" {
		return from, parsed.Address
	}
	return parsed.String(), parsed.Address
}

// The ONE place PLAIN-over-cleartext is decided: net/smtp refuses PLAIN on an
// unencrypted connection, and this must not work around that except on loopback,
// where there is no network to sniff.
func Auth(host, username, password string) (smtp.Auth, error) {
	username = strings.TrimSpace(username)
	switch {
	case username == "" && password == "":
		return nil, nil
	case username == "":
		return nil, errors.New("a password is set but no username — enter the mail server username (usually your email address), or clear the password if the server takes mail without a login")
	case password == "":
		return nil, errors.New("a username is set but no password — enter the mail server password (or an app password), or clear the username if the server takes mail without a login")
	}
	return smtp.PlainAuth("", username, password, host), nil
}

// Mirrors net/smtp's own notion of a local connection.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RFC 5322 form; a non-conforming Date is a common spam signal.
func DateHeader(now time.Time) string {
	return now.UTC().Format(time.RFC1123Z)
}

// RFC 5322 requires a globally unique id, and threading in most clients keys off
// it, so a duplicate makes two messages collapse into one conversation.
func NewMessageID(from string) string {
	_, addr := SplitSender(from)
	strip := strings.NewReplacer("\r", "", "\n", "", " ", "", "<", "", ">", "")
	domain := "localhost"
	if at := strings.LastIndex(addr, "@"); at >= 0 && at+1 < len(addr) {
		domain = strip.Replace(addr[at+1:])
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}

func dial(ctx context.Context, addr, host, mode string, auth smtp.Auth, guard bool) (*smtp.Client, error) {
	var conn net.Conn
	var err error
	dialer := &net.Dialer{}
	if guard {
		dialer.Control = hfnet.SSRFDialControl()
	}
	if mode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("smtp client: %w", err)
	}
	if mode == "starttls" {
		ok, _ := c.Extension("STARTTLS")
		switch {
		case ok:
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				c.Close()
				return nil, fmt.Errorf("starttls: %w", err)
			}
		case auth != nil && !isLoopbackHost(host):
			c.Close()
			return nil, fmt.Errorf("%s doesn't offer STARTTLS, so the login can't be sent encrypted — use port 465 with connection security \"implicit\", or \"none\" only for a trusted local relay", host)
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			c.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return c, nil
}

func Verify(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string) error {
	c, err := dial(ctx, addr, host, mode, auth, true)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, envelope := SplitSender(from); envelope != "" {
		if err := c.Mail(envelope); err != nil {
			return fmt.Errorf("mail from %s: %w", envelope, err)
		}
		if err := c.Reset(); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
	}
	return c.Quit()
}

func Send(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	return send(ctx, addr, host, mode, auth, from, to, msg, true)
}

func SendTrusted(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	return send(ctx, addr, host, mode, auth, from, to, msg, false)
}

func send(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte, guard bool) error {
	c, err := dial(ctx, addr, host, mode, auth, guard)
	if err != nil {
		return err
	}
	defer c.Close()

	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}
