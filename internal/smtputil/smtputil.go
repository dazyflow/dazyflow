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
	"net/smtp"
	"time"
)

// Send dials addr and delivers msg: implicit-TLS or plain dial by mode
// ("implicit" / "starttls" / "none"), opportunistic STARTTLS, optional
// AUTH, then MAIL/RCPT/DATA/QUIT. The connection inherits ctx's
// deadline (fallback 30s) so a black-holed server can't wedge the
// caller for the OS dial timeout.
func Send(ctx context.Context, addr, host, mode string, auth smtp.Auth, from string, to []string, msg []byte) error {
	var conn net.Conn
	var err error
	dialer := &net.Dialer{}
	if mode == "implicit" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: &tls.Config{ServerName: host}}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()

	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
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
