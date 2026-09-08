// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The IMAP client dance shared by the Mailbox drops.
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

const (
	ModeSTARTTLS = "starttls"
	ModeImplicit = "implicit"
	ModeNone     = "none"

	// Every IMAP server has INBOX; nothing else is guaranteed to exist.
	DefaultFolder = "INBOX"

	// go-imap takes no context, so a deadline is the only cancellation it has.
	defaultDeadline = 30 * time.Second
)

type Config struct {
	Host     string
	Port     int
	TLS      string
	Username string
	Password string
	Folder   string
}

func (c Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// The port a security mode implies.
func defaultPort(mode string) int {
	if mode == ModeImplicit {
		return 993
	}
	return 143
}

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

// The shape connection injection delivers.
func ConfigFromConn(conn map[string]string) (Config, error) {
	cfg := Config{
		Host:     strings.TrimSpace(conn["host"]),
		Username: strings.TrimSpace(conn["username"]),
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

type Client struct {
	*imapclient.Client

	conn      net.Conn
	stopWatch func()
}

// Logout is best-effort: the connection goes either way.
func (c *Client) Close() {
	if c == nil {
		return
	}
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	if c.Client != nil {
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

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// The caller owns Close.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("no mail server configured")
	}
	// A login over an unencrypted connection puts the password on the wire.
	if cfg.TLS == ModeNone && cfg.Password != "" && !isLoopbackHost(cfg.Host) {
		return nil, fmt.Errorf("refusing to send the mailbox password to %s unencrypted — set connection security to \"starttls\" (port 143) or \"implicit\" (port 993), or clear the login if the server takes no password", cfg.Host)
	}

	addr := cfg.Addr()
	if err := hfnet.CheckDialHost(addr); err != nil {
		return nil, err
	}

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

	// go-imap's commands take no context, so cancellation arrives as a deadline on
	// the underlying connection.
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

func newClient(conn net.Conn, cfg Config) (*imapclient.Client, error) {
	opts := &imapclient.Options{}
	switch cfg.TLS {
	case ModeImplicit:
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
			// Never authenticates when the upgrade fails.
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

// UIDValidity is what tells a stored watermark from a renumbered mailbox: if it
// changed, every stored UID means nothing.
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

func Verify(ctx context.Context, cfg Config) error {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Select(cfg.Folder, true)
	return err
}

func tlsConfigFor(host string) *tls.Config {
	return &tls.Config{ServerName: host}
}
