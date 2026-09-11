// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sshutil

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	xknownhosts "golang.org/x/crypto/ssh/knownhosts"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// HostKey is what a server offers before anybody has agreed to trust it.
type HostKey struct {
	Fingerprint string // SHA256:…, the form the Config field takes
	Type        string // ssh-ed25519, rsa-sha2-512, …
	KnownHosts  string // one known_hosts line for this address
}

// stopScan ends the handshake the moment the key is in hand. Nothing past the
// host key is wanted here, and going on would mean offering credentials to a
// server nobody has vouched for yet.
var stopScan = errors.New("host key captured")

// ScanHostKey reads the key a server offers, the way `ssh-keyscan` does: no
// credential, no trust decision, no authentication attempt.
//
// It exists because host-key checking has no default — a credential with
// neither fingerprint nor known_hosts refuses to connect — and the only other
// way to learn the value was to save a credential, watch it fail, and read the
// fingerprint out of the failure. That works, but it makes "set this server up"
// a loop through an error message. Scanning turns it into a step: here is what
// the server offered, compare it with what your provider published, accept it.
//
// Reading a key is not verifying it. Whoever answers this address supplies the
// answer, so the value is worth exactly as much as the comparison the operator
// makes against it — which is why this returns the key rather than storing it.
func ScanHostKey(ctx context.Context, host string, port int) (HostKey, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return HostKey{}, errors.New("enter the server's address first")
	}
	if port == 0 {
		port = DefaultPort
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if err := hfnet.CheckDialHost(addr); err != nil {
		return HostKey{}, err
	}

	dialer := &net.Dialer{Control: hfnet.SSRFDialControl()}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return HostKey{}, fmt.Errorf("couldn't reach %s: %w", addr, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(defaultDeadline))
	}

	var found ssh.PublicKey
	_, _, _, err = ssh.NewClientConn(conn, addr, &ssh.ClientConfig{
		User: "dazyflow-host-key-scan",
		// Ed25519 first, because that is the key providers publish and the one
		// OpenSSH prefers. Left to the library's own order a server offers its
		// ecdsa key, and the operator compares it against a published ed25519
		// fingerprint and concludes they are being attacked.
		HostKeyAlgorithms: []string{
			ssh.KeyAlgoED25519,
			ssh.KeyAlgoRSASHA512,
			ssh.KeyAlgoRSASHA256,
			ssh.KeyAlgoECDSA256,
			ssh.KeyAlgoECDSA384,
			ssh.KeyAlgoECDSA521,
			ssh.KeyAlgoRSA,
		},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			found = key
			return stopScan
		},
		Timeout: defaultDeadline,
	})
	_ = err // the handshake is meant to end at stopScan; the key is the result
	if found == nil {
		return HostKey{}, fmt.Errorf("%s answered, but not with an SSH host key — check the address and port", addr)
	}
	return HostKey{
		Fingerprint: ssh.FingerprintSHA256(found),
		Type:        found.Type(),
		KnownHosts:  xknownhosts.Line([]string{xknownhosts.Normalize(addr)}, found),
	}, nil
}

// GenerateKeyPair makes an ed25519 pair: the private half to store as the
// credential, the public half to paste into the server's authorized_keys.
//
// ed25519 and nothing else. It is the default OpenSSH generates today, every
// server that matters accepts it, and offering a choice of algorithm and size
// would be three more decisions on a screen whose whole point is having fewer.
func GenerateKeyPair(comment string) (privatePEM string, authorizedKey string, err error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, strings.TrimSpace(comment))
	if err != nil {
		return "", "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if c := strings.TrimSpace(comment); c != "" {
		line += " " + c
	}
	return string(pem.EncodeToMemory(block)), line, nil
}
