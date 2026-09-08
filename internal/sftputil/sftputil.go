// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// The SSH/SFTP connection dance shared by the SFTP drops.
package sftputil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

const (
	DefaultPort = 22

	defaultDeadline = 60 * time.Second
)

type Config struct {
	Host     string
	Port     int
	Username string

	Password   string
	PrivateKey string
	Passphrase string

	KnownHosts  string
	Fingerprint string

	Directory string
}

func (c Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

func ParsePort(s string) (int, error) {
	if s = strings.TrimSpace(s); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return 0, errors.New("port must be a number, e.g. 22")
		}
		return n, nil
	}
	return DefaultPort, nil
}

// The shape connection injection delivers.
func ConfigFromConn(conn map[string]string) (Config, error) {
	cfg := Config{
		Host:        strings.TrimSpace(conn["host"]),
		Username:    strings.TrimSpace(conn["username"]),
		Password:    conn["password"],
		PrivateKey:  conn["private_key"],
		Passphrase:  conn["passphrase"],
		KnownHosts:  conn["known_hosts"],
		Fingerprint: strings.TrimSpace(conn["fingerprint"]),
		Directory:   strings.TrimSpace(conn["directory"]),
	}
	if cfg.Host == "" {
		return Config{}, errors.New("enter the SFTP server's address")
	}
	if cfg.Username == "" {
		return Config{}, errors.New("enter the username to sign in with")
	}
	if strings.TrimSpace(cfg.Password) == "" && strings.TrimSpace(cfg.PrivateKey) == "" {
		return Config{}, errors.New("enter either a password or an SSH private key — the server needs one of them to let you in")
	}
	port, err := ParsePort(conn["port"])
	if err != nil {
		return Config{}, err
	}
	cfg.Port = port
	return cfg, nil
}

type Client struct {
	*sftp.Client

	ssh       *ssh.Client
	conn      net.Conn
	stopWatch func()
}

func (c *Client) Close() {
	if c == nil {
		return
	}
	if c.stopWatch != nil {
		c.stopWatch()
		c.stopWatch = nil
	}
	if c.Client != nil {
		_ = c.Client.Close()
		c.Client = nil
	}
	if c.ssh != nil {
		_ = c.ssh.Close()
		c.ssh = nil
	}
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
}

// Key first: a server that accepts both should not be handed the password.
func authMethods(cfg Config) ([]ssh.AuthMethod, error) {
	var out []ssh.AuthMethod
	if key := strings.TrimSpace(cfg.PrivateKey); key != "" {
		var signer ssh.Signer
		var err error
		if pass := strings.TrimSpace(cfg.Passphrase); pass != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(cfg.PrivateKey), []byte(pass))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(cfg.PrivateKey))
		}
		if err != nil {
			return nil, fmt.Errorf("that SSH private key couldn't be read: %w (a passphrase-protected key needs its passphrase filled in too)", err)
		}
		out = append(out, ssh.PublicKeys(signer))
	}
	if pw := cfg.Password; strings.TrimSpace(pw) != "" {
		out = append(out, ssh.Password(pw))
	}
	if len(out) == 0 {
		return nil, errors.New("no password or SSH private key configured")
	}
	return out, nil
}

// STRICT: an unpinned host key is refused, never trusted on first use.
func hostKeyCallback(cfg Config) (cb ssh.HostKeyCallback, algos []string, err error) {
	if kh := strings.TrimSpace(cfg.KnownHosts); kh != "" {
		db, err := knownHostsDB(kh)
		if err != nil {
			return nil, nil, fmt.Errorf("that known_hosts entry couldn't be read: %w", err)
		}
		return db.HostKeyCallback(), db.HostKeyAlgorithms(cfg.Addr()), nil
	}
	if want := cfg.Fingerprint; want != "" {
		return fingerprintCallback(want), nil, nil
	}
	return learnHostKey(cfg), nil, nil
}

func knownHostsDB(lines string) (*knownhosts.HostKeyDB, error) {
	f, err := os.CreateTemp("", "dazyflow-sftp-known-hosts-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(strings.TrimSpace(lines) + "\n"); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return knownhosts.NewDB(f.Name())
}

func fingerprintCallback(want string) ssh.HostKeyCallback {
	norm := func(s string) string {
		return strings.TrimPrefix(strings.TrimSpace(s), "SHA256:")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		got := ssh.FingerprintSHA256(key)
		if norm(got) == norm(want) {
			return nil
		}
		return fmt.Errorf("the server's SSH key is %s, but the connection expects %s — if the server was rebuilt or its key rotated, update the fingerprint; otherwise something is answering for this address that shouldn't be", got, want)
	}
}

// Never accepts: it reports the key so a human can pin it deliberately.
func learnHostKey(cfg Config) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return fmt.Errorf("%s hasn't been verified yet: its SSH key is %s. Copy that into \"Host key fingerprint\" on the SFTP page to accept it — check it against what your provider published, or against `ssh-keyscan %s`, before you do", cfg.Host, ssh.FingerprintSHA256(key), cfg.Host)
	}
}

func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("no SFTP server configured")
	}
	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	hostKey, algos, err := hostKeyCallback(cfg)
	if err != nil {
		return nil, err
	}

	addr := cfg.Addr()
	if err := hfnet.CheckDialHost(addr); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Control: hfnet.SSRFDialControl()}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(defaultDeadline))
	}

	// Neither library takes a context, so cancellation arrives as a deadline.
	closed := make(chan struct{})
	var once sync.Once
	stopWatch := func() { once.Do(func() { close(closed) }) }
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.SetDeadline(time.Now())
		case <-closed:
		}
	}()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User:              cfg.Username,
		Auth:              auth,
		HostKeyCallback:   hostKey,
		HostKeyAlgorithms: algos,
		Timeout:           defaultDeadline,
	})
	if err != nil {
		stopWatch()
		_ = conn.Close()
		return nil, sshError(cfg, err)
	}
	sshClient := ssh.NewClient(sshConn, chans, reqs)

	// No deadline on the transfer: a large file legitimately takes a long time.
	_ = conn.SetDeadline(time.Time{})

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		stopWatch()
		_ = sshClient.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("%s accepted the login but wouldn't start an SFTP session — some servers allow SSH without SFTP, or restrict it per account (%w)", cfg.Host, err)
	}
	return &Client{Client: sftpClient, ssh: sshClient, conn: conn, stopWatch: stopWatch}, nil
}

func sshError(cfg Config, err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		if strings.TrimSpace(cfg.PrivateKey) != "" {
			return fmt.Errorf("%s rejected the login — check the username, and that this key's public half is in the account's authorized_keys (%w)", cfg.Host, err)
		}
		return fmt.Errorf("%s rejected the username or password (%w)", cfg.Host, err)
	case strings.Contains(msg, "knownhosts: key mismatch"):
		return fmt.Errorf("%s offered a different SSH key than the one configured — if the server was rebuilt or rotated its key, update known_hosts; otherwise something is answering for this address that shouldn't be (%w)", cfg.Host, err)
	case strings.Contains(msg, "knownhosts: key is unknown"):
		return fmt.Errorf("%s isn't in the configured known_hosts — add its key there, or clear known_hosts and use the simpler \"Host key fingerprint\" field instead (%w)", cfg.Host, err)
	default:
		return fmt.Errorf("couldn't connect to %s: %w", cfg.Host, err)
	}
}

// The "Test connection" probe.
func Verify(ctx context.Context, cfg Config) error {
	c, err := Dial(ctx, cfg)
	if err != nil {
		return err
	}
	defer c.Close()

	dir := cfg.Directory
	if dir == "" {
		dir = "."
	}
	info, err := c.Stat(dir)
	if err != nil {
		return fmt.Errorf("signed in, but couldn't open the folder %q — check the path on the SFTP page (%w)", dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is a file, not a folder — the folder setting names the directory the steps work in", dir)
	}
	return nil
}
