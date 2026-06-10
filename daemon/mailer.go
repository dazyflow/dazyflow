package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// Mailer sends the platform's own transactional email — invitation
// links, failure notifications — through ONE operator-configured SMTP
// account (HAZYFLOW_SMTP_URL + HAZYFLOW_SMTP_FROM). This is deliberately
// separate from the Email drop: that one sends through each tenant's
// own mail server as a flow step; this one is daemon infrastructure,
// off until the operator wires it.
//
// URL shapes:
//
//	smtp://user:pass@mail.example.com:587            (STARTTLS, the default)
//	smtp://user:pass@mail.example.com:587?tls=none   (plaintext — local testing)
//	smtps://user:pass@mail.example.com:465           (implicit TLS)
//
// Credentials are optional (an internal relay may not need them).
type Mailer struct {
	From string

	host, port string
	tlsMode    string // "starttls" | "implicit" | "none"
	username   string
	password   string
	timeout    time.Duration
}

// NewMailerFromURL parses the operator's SMTP URL. Empty rawURL returns
// (nil, nil) — "not configured" is a normal state, not an error.
func NewMailerFromURL(rawURL, from string) (*Mailer, error) {
	if rawURL == "" {
		return nil, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("HAZYFLOW_SMTP_URL: %w", err)
	}
	m := &Mailer{From: from, timeout: 15 * time.Second}
	switch u.Scheme {
	case "smtp":
		m.tlsMode = "starttls"
	case "smtps":
		m.tlsMode = "implicit"
	default:
		return nil, fmt.Errorf("HAZYFLOW_SMTP_URL: scheme must be smtp:// or smtps:// (got %q)", u.Scheme)
	}
	if v := u.Query().Get("tls"); v != "" {
		switch v {
		case "starttls", "implicit", "none":
			m.tlsMode = v
		default:
			return nil, fmt.Errorf("HAZYFLOW_SMTP_URL: ?tls= must be starttls, implicit, or none")
		}
	}
	m.host = u.Hostname()
	if m.host == "" {
		return nil, fmt.Errorf("HAZYFLOW_SMTP_URL: host is required")
	}
	m.port = u.Port()
	if m.port == "" {
		if m.tlsMode == "implicit" {
			m.port = "465"
		} else {
			m.port = "587"
		}
	}
	if u.User != nil {
		m.username = u.User.Username()
		m.password, _ = u.User.Password()
	}
	if from == "" {
		// The login is usually the sender address — same fallback the
		// Email drop makes.
		m.From = m.username
	}
	if m.From == "" {
		return nil, fmt.Errorf("HAZYFLOW_SMTP_FROM is required (or put the sender as the URL's username)")
	}
	return m, nil
}

// Send delivers one plain-text message. Best-effort by contract: every
// caller treats a returned error as "log and move on" — transactional
// email must never fail the action that triggered it.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	addr := net.JoinHostPort(m.host, m.port)
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: m.timeout}
	if m.tlsMode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: m.host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(m.timeout))
	}

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	if m.tlsMode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if m.username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.username, m.password, m.host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("rcpt %s: %w", to, err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(mailerMessage(m.From, to, subject, body)); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close body: %w", err)
	}
	return c.Quit()
}

// mailerMessage assembles a plain-text RFC 822 message. Address headers
// are CRLF-stripped (header-injection defense); the subject is MIME-word
// encoded for non-ASCII.
func mailerMessage(from, to, subject, body string) []byte {
	strip := strings.NewReplacer("\r", "", "\n", "")
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", strip.Replace(from))
	fmt.Fprintf(&sb, "To: %s\r\n", strip.Replace(to))
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}
