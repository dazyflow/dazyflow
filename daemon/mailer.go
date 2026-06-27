// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/internal/emailtheme"
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
//
// DAZYFLOW_SMTP_FROM may carry a display name in RFC 5322 form so the
// recipient's client shows a friendly sender instead of the bare local
// part: "Dazyflow <hi@dazyflow.app>". The display name only rides the
// From: header — the SMTP envelope and the Message-ID domain always use
// the bare address parsed out of it.
type Mailer struct {
	From string // From: header, may include a display name

	addr       string // bare envelope sender (MAIL FROM, Message-ID domain)
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
	// Split the bare address out of From so the envelope and Message-ID
	// stay valid even when From carries a display name ("Dazyflow
	// <hi@dazyflow.app>"). With a display name, re-format through
	// mail.Address so it's MIME-encoded in the header when non-ASCII;
	// without one, keep the bare address verbatim (no angle brackets). A
	// From that doesn't parse (e.g. a bare hostless username) falls back
	// to using it as-is for both — same lenient behaviour as before.
	if parsed, err := mail.ParseAddress(m.From); err == nil {
		m.addr = parsed.Address
		if parsed.Name != "" {
			m.From = parsed.String()
		}
	} else {
		m.addr = m.From
	}
	return m, nil
}

// prepare applies the default send timeout (when the caller's ctx has no
// deadline) and builds the SMTP auth — the shared preamble of Send and
// sendHTML. The caller must defer the returned cancel.
func (m *Mailer) prepare(ctx context.Context) (context.Context, context.CancelFunc, smtp.Auth) {
	cancel := context.CancelFunc(func() {})
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
	}
	var auth smtp.Auth
	if m.username != "" {
		auth = smtp.PlainAuth("", m.username, m.password, m.host)
	}
	return ctx, cancel, auth
}

// Send delivers one plain-text message. Best-effort by contract: every
// caller treats a returned error as "log and move on" — transactional
// email must never fail the action that triggered it.
func (m *Mailer) Send(ctx context.Context, to, subject, body string) error {
	ctx, cancel, auth := m.prepare(ctx)
	defer cancel()
	return smtputil.Send(ctx, net.JoinHostPort(m.host, m.port), m.host, m.tlsMode,
		auth, m.addr, []string{to}, mailerMessage(m.From, m.addr, to, subject, body))
}

// emailLogoURL returns the absolute URL of the hosted brand mark for the
// email header ({PublicBaseURL}/logo.png), or "" when the deployment's public
// base URL is unknown — the theme then falls back to the inline SVG mark.
// Email clients require absolute image URLs, so a relative path is useless.
func emailLogoURL(publicBaseURL string) string {
	base := strings.TrimRight(publicBaseURL, "/")
	if base == "" {
		return ""
	}
	return base + "/logo.png"
}

// SendThemed renders c through the shared transactional theme and sends it as
// a multipart text+HTML message, using textBody as the plain-text
// alternative. The subject comes from c so the HTML <title> and the real
// Subject header stay in sync. Same best-effort contract as Send: callers
// log and move on.
func (m *Mailer) SendThemed(ctx context.Context, to, textBody string, c emailtheme.Content) error {
	htmlBody, err := emailtheme.Render(c)
	if err != nil {
		return err
	}
	return m.sendHTML(ctx, to, c.Subject, textBody, htmlBody)
}

// sendHTML delivers one multipart/alternative message: a text/plain part (for
// text-only clients and better deliverability) followed by a text/html part.
// Same transport + best-effort contract as Send.
func (m *Mailer) sendHTML(ctx context.Context, to, subject, text, htmlBody string) error {
	ctx, cancel, auth := m.prepare(ctx)
	defer cancel()
	msg, err := multipartMessage(m.From, m.addr, to, subject, text, htmlBody)
	if err != nil {
		return err
	}
	return smtputil.Send(ctx, net.JoinHostPort(m.host, m.port), m.host, m.tlsMode,
		auth, m.addr, []string{to}, msg)
}

// multipartMessage assembles a multipart/alternative RFC 822 message: the
// text/plain part first, the text/html part last (clients render the last
// part they understand). Both parts are quoted-printable encoded so HTML's
// long lines stay within SMTP's 998-octet line limit. Headers match
// mailerMessage — CRLF-stripped addresses (header-injection defense), a
// MIME-encoded subject, and a stable Date + Message-ID for dedup.
func multipartMessage(fromHeader, fromAddr, to, subject, text, htmlBody string) ([]byte, error) {
	strip := strings.NewReplacer("\r", "", "\n", "")
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}
	var sb bytes.Buffer
	fmt.Fprintf(&sb, "From: %s\r\n", strip.Replace(fromHeader))
	fmt.Fprintf(&sb, "To: %s\r\n", strip.Replace(to))
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&sb, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&sb, "Message-ID: %s\r\n", newMessageID(fromAddr))
	sb.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&sb, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)

	fmt.Fprintf(&sb, "--%s\r\n", boundary)
	sb.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	if err := writeQuotedPrintable(&sb, text); err != nil {
		return nil, err
	}
	sb.WriteString("\r\n")

	fmt.Fprintf(&sb, "--%s\r\n", boundary)
	sb.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	if err := writeQuotedPrintable(&sb, htmlBody); err != nil {
		return nil, err
	}
	sb.WriteString("\r\n")

	fmt.Fprintf(&sb, "--%s--\r\n", boundary)
	return sb.Bytes(), nil
}

// writeQuotedPrintable QP-encodes s into w. The encoder wraps lines with soft
// breaks (=CRLF) at 76 columns, keeping every line well under the SMTP limit.
func writeQuotedPrintable(w io.Writer, s string) error {
	qp := quotedprintable.NewWriter(w)
	if _, err := qp.Write([]byte(s)); err != nil {
		return err
	}
	return qp.Close()
}

// randomBoundary mints a MIME multipart boundary unlikely to collide with any
// byte sequence in the parts (128 random bits behind a fixed prefix).
func randomBoundary() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "dzf_" + hex.EncodeToString(b[:]), nil
}

// mailerMessage assembles a plain-text RFC 822 message. fromHeader is the
// full From: header (it may carry a display name); fromAddr is the bare
// sender address used to derive the Message-ID domain. Address headers
// are CRLF-stripped (header-injection defense); the subject is MIME-word
// encoded for non-ASCII.
func mailerMessage(fromHeader, fromAddr, to, subject, body string) []byte {
	strip := strings.NewReplacer("\r", "", "\n", "")
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\r\n", strip.Replace(fromHeader))
	fmt.Fprintf(&sb, "To: %s\r\n", strip.Replace(to))
	fmt.Fprintf(&sb, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	// Date + Message-ID are required by RFC 5322 and are what every
	// downstream MTA uses to collapse a retried submission into a single
	// delivery. Without a stable Message-ID, a transient resend (timeout,
	// greylisting, a relay re-queue) can't be de-duplicated and lands as a
	// duplicate copy. Relays that rewrite headers (e.g. Proton) supply
	// their own; setting ours covers the plain Postfix/SES relays that don't.
	fmt.Fprintf(&sb, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&sb, "Message-ID: %s\r\n", newMessageID(fromAddr))
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
