// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package imaputil holds the IMAP client dance shared by the Mailbox drops
// (drops/mailbox — search a folder, read one mail, take its attachments, mark
// it read) and the "Test connection" verifier behind the Mailbox integration
// page. Sibling to internal/smtputil, split out for exactly the reason that
// package gives: which port a security mode implies, when a login is allowed
// to cross the wire, and how a folder is selected must NOT drift between
// running a flow and testing the credentials that flow will use. A green
// "Test connection" that exercises a different handshake than the run is
// worse than no button.
//
// IMAP is the read side of email. SMTP (internal/smtputil, the Email drop)
// can only submit mail — the protocol has no command to list, fetch or search
// a mailbox — so everything that reads a mailbox comes through here.
package imaputil

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Connection security modes, spelled as the tenant picks them on the
// integration page. Same vocabulary as the Email drop's `tls` field so
// someone configuring both halves of one mail account meets one set of words.
const (
	ModeSTARTTLS = "starttls"
	ModeImplicit = "implicit"
	ModeNone     = "none"

	// DefaultFolder is what an unset folder means. Every IMAP server has
	// INBOX — it is the one mailbox name the RFC reserves and case-insensitively
	// guarantees — so it is a safe default rather than a guess.
	DefaultFolder = "INBOX"

	// defaultDeadline bounds a whole connection's I/O when the caller's context
	// carries no deadline. go-imap's commands take no context, so the deadline
	// on the conn is what stops a wedged server from parking a run forever.
	defaultDeadline = 30 * time.Second
)

// Config is one tenant's mailbox connection. It is the ConnectionFields
// bundle from the Mailbox integration page, parsed and defaulted.
type Config struct {
	Host     string
	Port     int
	TLS      string
	Username string
	Password string
	Folder   string
}

// Addr is the host:port to dial.
func (c Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// defaultPort is the port a security mode implies: 993 for implicit TLS, 143
// for STARTTLS or a cleartext connection. Reached only through ParsePort,
// which is the seam the drop and the verifier share — a verifier that
// defaulted to 143 while the run used 993 would test a connection nobody has.
func defaultPort(mode string) int {
	if mode == ModeImplicit {
		return 993
	}
	return 143
}

// ParseMode validates a security mode and supplies the default. STARTTLS is
// the default rather than implicit TLS to match the Email drop's `tls` field,
// so the two halves of one account don't disagree about what blank means.
func ParseMode(s string) (string, error) {
	switch mode := strings.TrimSpace(s); mode {
	case "":
		return ModeSTARTTLS, nil
	case ModeSTARTTLS, ModeImplicit, ModeNone:
		return mode, nil
	default:
		return "", errors.New(`connection security must be "starttls", "implicit", or "none"`)
	}
}

// ParsePort turns the configured port into a number, falling back to the
// mode's default when blank. ConnectionFields store every value as a string,
// so this is the one place that decides what "" and a non-number mean.
func ParsePort(s, mode string) (int, error) {
	if s = strings.TrimSpace(s); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, errors.New("port must be a number, e.g. 993 or 143")
		}
		return n, nil
	}
	return defaultPort(mode), nil
}

// ConfigFromConn builds a Config from a stored connection map — the shape a
// connection verifier is handed. Validation is deliberately front-loaded here
// (rather than left to the server) for the fields whose failure would
// otherwise surface as an opaque protocol error much later, mid-run.
func ConfigFromConn(conn map[string]string) (Config, error) {
	cfg := Config{
		Host:     strings.TrimSpace(conn["host"]),
		Username: strings.TrimSpace(conn["username"]),
		// A password is deliberately NOT trimmed — leading/trailing spaces can
		// be part of it (same call as smtputil.Auth makes).
		Password: conn["password"],
		Folder:   strings.TrimSpace(conn["folder"]),
	}
	if cfg.Host == "" {
		return Config{}, errors.New("enter your mail server")
	}
	mode, err := ParseMode(conn["tls"])
	if err != nil {
		return Config{}, err
	}
	cfg.TLS = mode
	port, err := ParsePort(conn["port"], mode)
	if err != nil {
		return Config{}, err
	}
	cfg.Port = port
	if cfg.Folder == "" {
		cfg.Folder = DefaultFolder
	}
	return cfg, nil
}

// Client is a logged-in IMAP connection plus the two things go-imap's client
// does not own: the socket (so a cancelled context can break an in-flight
// command) and the watcher goroutine doing that. Always Close() it.
type Client struct {
	*imapclient.Client

	conn      net.Conn
	stopWatch func()
}

// Close logs out politely and drops the connection. Logout is best-effort:
// on a connection the server has already broken it would only produce a
// second error nobody can act on, and the socket has to be released either
// way. Safe to call more than once.
func (c *Client) Close() {
	if c == nil {
		return
	}
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	if c.Client != nil {
		// A short deadline of its own: a wedged server must not turn cleanup
		// into a second hang after the work already succeeded.
		_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
		_ = c.Client.Logout().Wait()
		_ = c.Client.Close()
		c.Client = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// isLoopbackHost reports whether host names this machine. It carries the same
// exemption net/smtp's PlainAuth makes for localhost (and therefore
// smtputil.dial), so the cleartext-login refusal below doesn't turn away a
// local mail bridge on 127.0.0.1 — which has no network to sniff.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Dial opens a connection, upgrades it per cfg.TLS, and logs in. The caller
// owns Close() of the returned client.
//
// The dialer carries the shared SSRF Control hook, so the IP actually
// connected to is re-checked at dial time on the RESOLVED address — not just
// in the CheckDialHost pre-flight. Without it a tenant-supplied hostname
// passes the pre-flight (public IP) and then re-resolves to
// loopback/private/link-local/metadata at connect time (DNS rebinding), which
// here would hand the mailbox credentials to the rebind target. Same guard the
// SMTP, DB, MQTT and HTTP paths carry; it no-ops once the operator has opted
// into private egress.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("no mail server configured")
	}
	// A login over an unencrypted connection would put the mailbox password on
	// the wire in the clear. net/smtp's PlainAuth refuses that outright and so,
	// transitively, does the Email drop; IMAP has no such built-in, so the same
	// refusal is made explicit here. Naming both real fixes matters because
	// "none" is a deliberate choice someone made on the integration page, not a
	// typo — they need to know which way out they have.
	if cfg.TLS == ModeNone && cfg.Password != "" && !isLoopbackHost(cfg.Host) {
		return nil, fmt.Errorf("refusing to send the mailbox password to %s unencrypted — set connection security to \"starttls\" (port 143) or \"implicit\" (port 993), or clear the login if the server takes no password", cfg.Host)
	}

	addr := cfg.Addr()
	if err := hfnet.CheckDialHost(addr); err != nil {
		return nil, err
	}

	// Implicit TLS is negotiated by the dialer (port 993 speaks TLS from the
	// first byte); STARTTLS and none dial plaintext and upgrade — or don't —
	// in newClient. Mirrors smtputil.dial.
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Control: hfnet.SSRFDialControl()}
	if cfg.TLS == ModeImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfigFor(cfg.Host)}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(defaultDeadline))
	}

	// go-imap's commands take no context, so cancellation has to arrive as a
	// closed socket. Without this a cancelled run (and the adversarial
	// cancelled-context sweep in drops/invariants_test.go) would block on the
	// conn deadline instead of returning promptly.
	//
	// The watcher waits on ctx OR its own done channel, rather than on a
	// derived context: a derived one would report Canceled when Close stopped
	// it too, so an ordinary hang-up would race the deadline to now and cut
	// short the LOGOUT window Close deliberately opens.
	closed := make(chan struct{})
	var closeOnce sync.Once
	stopWatch := func() { closeOnce.Do(func() { close(closed) }) }
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-closed:
		}
	}()

	client, err := newClient(conn, cfg)
	if err != nil {
		stopWatch()
		_ = conn.Close()
		return nil, err
	}

	c := &Client{Client: client, conn: conn, stopWatch: stopWatch}
	if cfg.Username != "" || cfg.Password != "" {
		if err := c.Login(cfg.Username, cfg.Password).Wait(); err != nil {
			c.Close()
			return nil, fmt.Errorf("the mail server rejected the login: %w", err)
		}
	}
	return c, nil
}

// newClient wraps a dialed connection in the protocol client, doing the TLS
// upgrade the mode calls for.
func newClient(conn net.Conn, cfg Config) (*imapclient.Client, error) {
	opts := &imapclient.Options{}
	switch cfg.TLS {
	case ModeImplicit:
		// The dialer already negotiated TLS, so the socket carries no
		// plaintext and the client just wraps it.
		client := imapclient.New(conn, opts)
		if err := client.WaitGreeting(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("no IMAP greeting from %s: %w", cfg.Host, err)
		}
		return client, nil
	case ModeSTARTTLS:
		opts.TLSConfig = tlsConfigFor(cfg.Host)
		client, err := imapclient.NewStartTLS(conn, opts)
		if err != nil {
			// NewStartTLS closes the conn and never authenticates when the
			// upgrade fails, so the password has not left the machine. Say what
			// happened in the terms the integration page uses — a raw "BAD
			// STARTTLS" reads like a client bug rather than a setting to change.
			return nil, fmt.Errorf("%s wouldn't start an encrypted connection on port %d, so the login can't be sent safely — try connection security \"implicit\" on port 993, or \"none\" only for a trusted local server (%w)", cfg.Host, cfg.Port, err)
		}
		return client, nil
	default:
		client := imapclient.New(conn, opts)
		if err := client.WaitGreeting(); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("no IMAP greeting from %s: %w", cfg.Host, err)
		}
		return client, nil
	}
}

// Select opens a folder and returns its state — UIDValidity and UIDNext are
// what the "only new since last run" watermark is built on.
//
// readOnly picks EXAMINE over SELECT. Every drop that only reads passes true,
// so merely running a search can never change a flag on the server: an
// EXAMINE-ed mailbox is guaranteed not to have \Seen set as a side effect,
// which is the difference between a triage flow that reads a mailbox and one
// that quietly marks it all read.
func (c *Client) Select(folder string, readOnly bool) (*imap.SelectData, error) {
	if folder == "" {
		folder = DefaultFolder
	}
	data, err := c.Client.Select(folder, &imap.SelectOptions{ReadOnly: readOnly}).Wait()
	if err != nil {
		return nil, fmt.Errorf("can't open the folder %q — check the name on the Mailbox page (%w)", folder, err)
	}
	return data, nil
}

// Verify is the "Test connection" probe: connect, log in, open the folder,
// and hang up without reading a single message. It exercises every part of
// the configuration a run depends on — host, port, security mode, login, and
// the folder name, which is the field most likely to be quietly wrong.
func Verify(ctx context.Context, cfg Config) error {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Select(cfg.Folder, true)
	return err
}

// tlsConfigFor is the TLS configuration every encrypted path uses. ServerName
// is set explicitly so certificate verification is pinned to the configured
// hostname rather than whatever the connection resolved to — the other half
// of the rebinding guard on the dialer.
func tlsConfigFor(host string) *tls.Config {
	return &tls.Config{ServerName: host}
}
