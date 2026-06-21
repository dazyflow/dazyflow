package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/internal/smtputil"
)

// Mailer sends the platform's own transactional email — invitation
// links, failure notifications — through ONE operator-configured SMTP
// account (DAZYFLOW_SMTP_URL + DAZYFLOW_SMTP_FROM). This is deliberately
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
		return nil, fmt.Errorf("DAZYFLOW_SMTP_URL: %w", err)
	}
	m := &Mailer{From: from, timeout: 15 * time.Second}
	switch u.Scheme {
	case "smtp":
		m.tlsMode = "starttls"
	case "smtps":
		m.tlsMode = "implicit"
	default:
		return nil, fmt.Errorf("DAZYFLOW_SMTP_URL: scheme must be smtp:// or smtps:// (got %q)", u.Scheme)
	}
	if v := u.Query().Get("tls"); v != "" {
		switch v {
		case "starttls", "implicit", "none":
			m.tlsMode = v
		default:
			return nil, fmt.Errorf("DAZYFLOW_SMTP_URL: ?tls= must be starttls, implicit, or none")
		}
	}
	m.host = u.Hostname()
	if m.host == "" {
		return nil, fmt.Errorf("DAZYFLOW_SMTP_URL: host is required")
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
		return nil, fmt.Errorf("DAZYFLOW_SMTP_FROM is required (or put the sender as the URL's username)")
	}
	return m, nil
}

// Send delivers one plain-text message. Best-effort by contract: every
// caller treats a returned error as "log and move on" — transactional
// email must never fail the action that triggered it.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
		defer cancel()
	}
	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}
	return smtputil.Send(ctx, net.JoinHostPort(m.host, m.port), m.host, m.tlsMode,
		auth, m.From, []string{to}, mailerMessage(m.From, to, subject, body))
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
	// Date + Message-ID are required by RFC 5322 and are what every
	// downstream MTA uses to collapse a retried submission into a single
	// delivery. Without a stable Message-ID, a transient resend (timeout,
	// greylisting, a relay re-queue) can't be de-duplicated and lands as a
	// duplicate copy. Relays that rewrite headers (e.g. Proton) supply
	// their own; setting ours covers the plain Postfix/SES relays that don't.
	fmt.Fprintf(&sb, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&sb, "Message-ID: %s\r\n", newMessageID(from))
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	sb.WriteString(body)
	return []byte(sb.String())
}

// newMessageID mints a unique RFC 5322 Message-ID of the form
// <random-hex@sender-domain>. The domain comes from the sender so the ID
// is well-formed on relays that validate it; the 128 random bits make
// collisions across sends effectively impossible.
func newMessageID(from string) string {
	strip := strings.NewReplacer("\r", "", "\n", "", " ", "")
	domain := "localhost"
	if at := strings.LastIndex(from, "@"); at >= 0 && at+1 < len(from) {
		domain = strip.Replace(from[at+1:])
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is near-impossible; fall back to a
		// time-based token so the header stays present and unique-enough.
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}
