// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sftputil holds the SSH/SFTP connection dance shared by the SFTP
// drops (drops/sftpfiles — list, download, upload) and the "Test connection"
// verifier behind the SFTP integration page. Sibling to smtputil and
// imaputil, split out for the same reason: how a host key is checked and
// which auth method is offered must NOT drift between running a flow and
// testing the credentials that flow will use.
//
// SFTP is the file-transfer protocol that corporate integration actually runs
// on — bank files, payroll, EDI, supplier catalogues — and it is a protocol
// rather than a vendor, so one connector reaches every server that speaks it.
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
	// DefaultPort is SSH's port; SFTP is a subsystem of SSH, not a protocol
	// with a port of its own.
	DefaultPort = 22

	// defaultDeadline bounds a whole connection's I/O when the caller's
	// context carries no deadline.
	defaultDeadline = 60 * time.Second
)

// Config is one tenant's SFTP server, parsed and defaulted from the
// ConnectionFields bundle on the integration page.
type Config struct {
	Host     string
	Port     int
	Username string

	// Password and PrivateKey are alternative auth methods; at least one must
	// be set. A key is offered before a password when both are.
	Password   string
	PrivateKey string
	Passphrase string

	// KnownHosts is one or more OpenSSH known_hosts lines. Fingerprint is the
	// simpler alternative — an "SHA256:…" string as printed by ssh-keyscan or
	// published in a provider's docs. Either satisfies host-key verification;
	// see hostKeyCallback for why one of them is mandatory.
	KnownHosts  string
	Fingerprint string

	// Directory is the remote folder the steps work in by default.
	Directory string
}

// Addr is the host:port to dial.
func (c Config) Addr() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// ParsePort turns the configured port into a number, defaulting to 22.
// ConnectionFields store every value as a string, so this is the one place
// that decides what "" and a non-number mean.
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

// ConfigFromConn builds a Config from a stored connection map — the shape a
// connection verifier is handed. Validation is front-loaded here for the
// fields whose failure would otherwise surface as an opaque protocol error
// much later, mid-run.
func ConfigFromConn(conn map[string]string) (Config, error) {
	cfg := Config{
		Host:     strings.TrimSpace(conn["host"]),
		Username: strings.TrimSpace(conn["username"]),
		// Not trimmed: leading/trailing spaces can be part of a password, and
		// a private key is whitespace-significant.
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

// Client is a logged-in SFTP session plus the two things the sftp package
// does not own: the socket (so a cancelled context can break an in-flight
// transfer) and the watcher goroutine doing that. Always Close() it.
type Client struct {
	*sftp.Client

	ssh       *ssh.Client
	conn      net.Conn
	stopWatch func()
}

// Close tears the session down. Best-effort throughout: the socket has to be
// released whatever the SFTP or SSH layer thinks. Safe to call twice.
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

// authMethods offers a private key first, then a password. Order matters:
// a server configured for keys often also accepts passwords for a different
// account, and offering the key first means the intended credential is the
// one that authenticates.
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

// hostKeyCallback builds strict host-key verification, plus the host-key
// algorithms to advertise.
//
// There is deliberately NO "accept any host key" path. Over SSH that is
// silent MITM: an attacker who can answer for the address gets the password
// or a signature, and the transfer besides. drops/git takes the same line.
//
// Unlike git, though, there is no useful set of keys to bundle — a tenant's
// SFTP server is theirs, so its key can only come from them. That would
// normally mean asking a non-technical user to paste a known_hosts line
// before anything works at all, which is why an "SHA256:…" fingerprint is
// accepted as the friendlier equivalent, and why the unconfigured case
// (learnHostKey) fails with the server's ACTUAL fingerprint in the message
// for them to copy. Fail closed, but say exactly what to paste.
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

// knownHostsDB parses known_hosts lines into a verification database.
// skeema/knownhosts reads files, so the lines go through a temp file which is
// removed as soon as the database is built (it reads eagerly into memory).
// Same dance drops/git does, for the same reason.
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

// fingerprintCallback accepts exactly the one key whose SHA256 fingerprint
// was configured. Comparison tolerates a missing "SHA256:" prefix, since
// that is how half the tools print it.
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

// learnHostKey never accepts. It reads the key the server offered and refuses
// with it quoted, so the fix is a copy and paste rather than a hunt for
// ssh-keyscan. The connection is torn down before authentication, so no
// credential reaches an unverified host.
func learnHostKey(cfg Config) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return fmt.Errorf("%s hasn't been verified yet: its SSH key is %s. Copy that into \"Host key fingerprint\" on the SFTP page to accept it — check it against what your provider published, or against `ssh-keyscan %s`, before you do", cfg.Host, ssh.FingerprintSHA256(key), cfg.Host)
	}
}

// Dial opens an SSH connection, verifies the host key, authenticates, and
// starts an SFTP session. The caller owns Close() of the returned client.
//
// The dialer carries the shared SSRF Control hook, so the IP actually
// connected to is re-checked at dial time on the resolved address — without
// it a tenant-supplied hostname passes the CheckDialHost pre-flight on a
// public IP and then re-resolves to loopback/private/metadata at connect time
// (DNS rebinding), which here would hand the server credentials to the
// rebind target. Same guard the SMTP, IMAP, DB and MQTT paths carry.
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

	// Neither x/crypto/ssh nor pkg/sftp takes a context, so cancellation has
	// to arrive as a dead socket. Without this a cancelled run — and the
	// adversarial cancelled-context sweep in drops/invariants_test.go — would
	// block until the deadline instead of returning promptly. The watcher
	// waits on its own channel as well as ctx, so an ordinary Close doesn't
	// race the deadline to now.
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

	// The transfer itself has no deadline: a large file legitimately takes
	// longer than the handshake, and the step's own timeout plus the ctx
	// watcher above are what bound it.
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

// sshError translates the handshake's failures into the words of the form
// someone filled in. A bare "ssh: handshake failed" reads like a client bug.
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

// Verify is the "Test connection" probe: connect, authenticate, and stat the
// configured folder — then hang up without transferring anything. The folder
// is included because it is the field most likely to be quietly wrong, and a
// mistyped path otherwise fails per-run, deep inside a flow, where nothing
// points back at the integration page.
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
