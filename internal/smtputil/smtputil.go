// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smtputil holds the SMTP client dance shared by the Email drop
// (drops/notify, sends through the TENANT's mail server as a flow step)
// and the daemon's transactional Mailer (one OPERATOR account for
// invites and failure notifications). The two stay separately
// configured on purpose; the wire protocol below is the part that must
// not drift between them.
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

// SplitSender separates a configured sender into the two forms one send needs.
// A sender may carry a display name in RFC 5322 form ("Reports
// <reports@example.com>") so the recipient's client shows a friendly name —
// that belongs in the From: header, but NOT in the SMTP envelope: a
// reverse-path of "<Reports <reports@example.com>>" is not a valid MAIL FROM
// and servers reject the whole send. Pass header to the message builder and
// envelope to Send/SendTrusted.
//
// When a display name is present the header form is re-rendered through
// mail.Address so a non-ASCII name is MIME-encoded rather than riding as raw
// UTF-8 bytes. Without one the input is returned verbatim, so a bare address
// keeps exactly the shape it was typed in. A sender that doesn't parse at all
// (e.g. a hostless username for an internal relay) is returned verbatim for
// both, leaving it to the mail server to accept or refuse — every caller here
// was lenient about that before the split and stays so.
//
// Shared by all three senders — the Email drop, the email-template test send,
// and the operator's transactional Mailer — so the header/envelope split
// can't drift between them.
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

// Auth builds the SMTP authenticator for a configured login, and is the ONE
// place that decides when a send goes out unauthenticated. Both fields empty
// means "this relay takes mail without a login" — a legitimate setup (an
// internal sidecar relay), so it returns a nil Auth and no error.
//
// A PARTIALLY filled login is an error, not a nil Auth. It used to be silent:
// every caller built its own `if username != "" { PlainAuth(...) }`, so a
// connection carrying a password but no username threw the password away, sent
// AUTH-less, and reported success — the server either accepted the mail
// unauthenticated or, on the "Test connection" path, was never asked to rule on
// the credentials at all. Someone whose credentials had just been rotated saw a
// green OK for a login that was never presented. Failing loud here means the
// only way to reach a nil Auth is to configure no login at all.
//
// The message names the missing field, since the fix is to fill it in — and
// mentions clearing the other one, because a relay that wants no login is the
// other valid resolution.
func Auth(host, username, password string) (smtp.Auth, error) {
	username = strings.TrimSpace(username)
	// A password is deliberately NOT trimmed — leading/trailing spaces can be
	// part of it. Only its emptiness matters here.
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

// isLoopbackHost reports whether host names this machine. It mirrors the
// exemption net/smtp's PlainAuth makes for localhost, so the STARTTLS
// requirement in dial doesn't refuse a local relay (a mail bridge on
// 127.0.0.1) that net/smtp would itself have allowed to authenticate in the
// clear.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DateHeader is the value of a message's Date: header, in the RFC 5322 form
// every MTA parses. Trivial, but it lives next to NewMessageID because the two
// headers are only useful together — see NewMessageID for why they matter.
func DateHeader(now time.Time) string {
	return now.UTC().Format(time.RFC1123Z)
}

// NewMessageID mints a unique RFC 5322 Message-ID of the form
// <random-hex@sender-domain>. The domain comes from the sender so the ID is
// well-formed on relays that validate it; the 128 random bits make collisions
// across sends effectively impossible.
//
// Date + Message-ID are required by RFC 5322 and are what every downstream MTA
// uses to collapse a retried submission into a single delivery. Without a
// stable Message-ID, a transient resend (timeout, greylisting, a relay
// re-queue) can't be de-duplicated and lands as a duplicate copy — and a relay
// that mints its own fresh ID per attempt makes the copies look like distinct
// messages to every later hop. Relays that rewrite headers (e.g. Proton)
// supply their own; setting ours covers the plain Postfix/SES relays that
// don't.
//
// Shared by all three senders — the Email drop, the email-template test send,
// and the operator's transactional Mailer — for the same reason SplitSender is:
// a header this load-bearing must not be present on one path and missing on
// another.
func NewMessageID(from string) string {
	// Callers pass the bare envelope address, but take the display-name form
	// too: SplitSender is the one place that knows how to get the address out
	// of "Reports <reports@example.com>", and a trailing ">" left in the
	// domain is a malformed header on every relay that validates one.
	_, addr := SplitSender(from)
	strip := strings.NewReplacer("\r", "", "\n", "", " ", "", "<", "", ">", "")
	domain := "localhost"
	if at := strings.LastIndex(addr, "@"); at >= 0 && at+1 < len(addr) {
		domain = strip.Replace(addr[at+1:])
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is near-impossible; fall back to a
		// time-based token so the header stays present and unique-enough.
		return fmt.Sprintf("<%d@%s>", time.Now().UnixNano(), domain)
	}
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}

// dial runs the shared front of the SMTP dance: dial addr (implicit-TLS or
// plain by mode), apply ctx's deadline (fallback 30s), open a client,
// STARTTLS (opportunistic, but mandatory once a login is in play — see the
// mode=="starttls" branch), and AUTH when auth is non-nil. Whether a nil auth
// is legitimate is decided by Auth, not here. Both Send and Verify build on it
// so the handshake can't drift between sending a message and testing a
// connection. The caller owns Close()/Quit() of the returned client.
//
// When guard is true the dialer carries the shared SSRF Control hook, so the
// IP we actually connect to is re-checked at dial time, on the resolved
// address — not just in the caller's pre-flight CheckDialHost. Without it, an
// attacker-controlled DNS name passes the pre-flight (public IP) and then
// re-resolves to loopback/private/link-local/metadata at connect time (DNS
// rebinding / TOCTOU), which would also exfiltrate the configured SMTP AUTH
// credentials to the rebind target. Tenant-supplied SMTP servers (the
// email_send drop and the "Test connection" button) MUST pass guard=true;
// this matches the dial guard already on the DB (drops/db), MQTT (drops/mqtt)
// and Vault paths. The operator's own transactional Mailer passes guard=false
// via SendTrusted because its host comes from trusted instance config and
// legitimately points at an internal/sidecar relay. When the operator has
// opted into private egress the hook no-ops anyway, same as the HTTP drops.
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
			// STARTTLS was asked for, the server doesn't offer it, and we hold a
			// login: sending it now would put the password on the wire in the
			// clear. net/smtp's PlainAuth refuses this too, but only with a bare
			// "unencrypted connection" that reads like a client bug — say what
			// actually happened and what the two real fixes are. Loopback is
			// exempt for the same reason PlainAuth exempts it: a local mail
			// bridge has no network to sniff.
			c.Close()
			return nil, fmt.Errorf("%s doesn't offer STARTTLS, so the login can't be sent encrypted — use port 465 with connection security \"implicit\", or \"none\" only for a trusted local relay", host)
		}
		// No STARTTLS and no login: the mail goes out in the clear, which is
		// what an unauthenticated internal relay wants. Left as-is on purpose —
		// making it an error would break relays that have always worked with
		// the default "starttls" setting and no TLS on the wire.
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			c.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return c, nil
}

// Verify confirms a mail server is reachable, that (when auth is set) the login
// is accepted, and that the server will take mail FROM the configured sender.
// It runs the dial/STARTTLS/AUTH handshake, then MAIL FROM + RSET, then QUIT.
// Drives the Email integration's "Test connection" button. The target is
// tenant-supplied, so the SSRF dial guard is always applied.
//
// The MAIL FROM probe is what makes a pass mean something. The handshake alone
// proves nothing about authorization when no login is configured — it QUITs
// having only said EHLO, so a server that refuses every unauthenticated
// submission still answered "ok" to everything we asked, and the button went
// green. Every big provider (Microsoft 365's 5.7.57, Gmail, Exim) rejects at
// MAIL FROM when the session isn't authenticated or the sender isn't one this
// login may use, so this is the cheapest command that can actually say no.
//
// Nothing is delivered: RSET abandons the transaction before any recipient or
// DATA, so no message and no recipient probe reaches anyone. An empty `from`
// skips the probe and keeps the old handshake-only behavior, for a caller that
// has no sender to offer.
func Verify(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string) error {
	c, err := dial(ctx, addr, host, mode, auth, true)
	if err != nil {
		return err
	}
	defer c.Close()
	// A display name is not a valid reverse-path — the envelope form only,
	// exactly as Send uses it (see SplitSender).
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

// Send dials addr and delivers msg: implicit-TLS or plain dial by mode
// ("implicit" / "starttls" / "none"), opportunistic STARTTLS, optional
// AUTH, then MAIL/RCPT/DATA/QUIT. The connection inherits ctx's
// deadline (fallback 30s) so a black-holed server can't wedge the
// caller for the OS dial timeout. The target is tenant-supplied (the
// email_send drop), so the SSRF dial guard is always applied; the operator's
// own Mailer uses SendTrusted instead.
func Send(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	return send(ctx, addr, host, mode, auth, from, to, msg, true)
}

// SendTrusted is Send for the operator's own transactional Mailer, whose SMTP
// host comes from trusted instance configuration (not tenant input) and may
// legitimately be an internal/sidecar relay. It skips the SSRF dial guard for
// that reason; never call it with a tenant-supplied host.
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
