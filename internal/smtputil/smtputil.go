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
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
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

// dial runs the shared front of the SMTP dance: dial addr (implicit-TLS or
// plain by mode), apply ctx's deadline (fallback 30s), open a client,
// opportunistic STARTTLS, and optional AUTH. Both Send and Verify build on it
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
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				c.Close()
				return nil, fmt.Errorf("starttls: %w", err)
			}
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

// Verify confirms a mail server is reachable and (when auth is set) the login
// is accepted, by running the dial/STARTTLS/AUTH handshake and then QUIT — no
// message is sent. Drives the Email integration's "Test connection" button.
// The target is tenant-supplied, so the SSRF dial guard is always applied.
func Verify(ctx context.Context, addr, host, mode string, auth smtp.Auth) error {
	c, err := dial(ctx, addr, host, mode, auth, true)
	if err != nil {
		return err
	}
	defer c.Close()
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
