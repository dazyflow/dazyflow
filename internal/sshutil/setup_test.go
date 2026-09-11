// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package sshutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

const scanUser, scanPass = "deploy", "s3cret"

// startSSH is a real server, because the host key only exists in a real
// handshake: what ScanHostKey reads is what the key exchange offers, and a
// fake would be asserting our own assumption back at us.
func startSSH(t *testing.T) (host string, port int, fingerprint string) {
	t.Helper()
	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == scanUser && string(pass) == scanPass {
				return nil, nil
			}
			return nil, os.ErrPermission
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for ch := range chans {
					_ = ch.Reject(ssh.Prohibited, "nothing to do here")
				}
			}()
		}
	}()

	h, p, _ := net.SplitHostPort(ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return h, n, ssh.FingerprintSHA256(signer.PublicKey())
}

func TestScanHostKey_ReadsWhatTheServerOffers(t *testing.T) {
	host, port, want := startSSH(t)

	got, err := ScanHostKey(t.Context(), host, port)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.Fingerprint != want {
		t.Errorf("fingerprint = %q, want %q", got.Fingerprint, want)
	}
	if got.Type != ssh.KeyAlgoED25519 {
		t.Errorf("key type = %q, want %q", got.Type, ssh.KeyAlgoED25519)
	}
	if !strings.Contains(got.KnownHosts, "ssh-ed25519") {
		t.Errorf("known_hosts line looks wrong: %q", got.KnownHosts)
	}
}

// The point of showing a fingerprint is that accepting it works. Both halves
// of the scan are pins, so both are dialled back here.
func TestScanHostKey_OutputPinsTheServer(t *testing.T) {
	host, port, _ := startSSH(t)
	scanned, err := ScanHostKey(t.Context(), host, port)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	base := Config{Host: host, Port: port, Username: scanUser, Password: scanPass}

	dial := func(what string, cfg Config) {
		c, err := DialSSH(t.Context(), cfg)
		if err != nil {
			t.Errorf("the scanned %s does not accept the server it came from: %v", what, err)
			return
		}
		c.Close()
	}

	byFingerprint := base
	byFingerprint.Fingerprint = scanned.Fingerprint
	dial("fingerprint", byFingerprint)

	byKnownHosts := base
	byKnownHosts.KnownHosts = scanned.KnownHosts
	dial("known_hosts line", byKnownHosts)
}

// A scan is not an authentication attempt: the handshake stops at the key, so
// a wrong username or no credential at all still returns one.
func TestScanHostKey_NeedsNoCredential(t *testing.T) {
	host, port, want := startSSH(t)
	got, err := ScanHostKey(t.Context(), host, port)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if got.Fingerprint != want {
		t.Errorf("fingerprint = %q, want %q", got.Fingerprint, want)
	}
}

func TestScanHostKey_SaysSoWhenNothingAnswers(t *testing.T) {
	hfnet.SetAllowPrivateEgress(true)
	t.Cleanup(func() { hfnet.SetAllowPrivateEgress(false) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	host, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	_ = ln.Close() // nothing is listening there now

	if _, err := ScanHostKey(t.Context(), host, port); err == nil {
		t.Fatal("scanning a dead port reported success")
	}

	if _, err := ScanHostKey(t.Context(), "  ", 22); err == nil {
		t.Error("scanning a blank address reported success")
	}
}

func TestGenerateKeyPair_HalvesMatch(t *testing.T) {
	priv, pub, err := GenerateKeyPair("dazyflow web-1")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	signer, err := ssh.ParsePrivateKey([]byte(priv))
	if err != nil {
		t.Fatalf("the private half does not parse: %v", err)
	}
	parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		t.Fatalf("the public half is not an authorized_keys line: %v", err)
	}
	if comment != "dazyflow web-1" {
		t.Errorf("comment = %q, want the one asked for", comment)
	}
	if ssh.FingerprintSHA256(parsed) != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Error("the public line does not belong to the private key")
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Errorf("key type = %q, want ed25519", signer.PublicKey().Type())
	}
}

// The stored form is what putSSHCredential parses back, so the round trip is
// the contract: generate here, save there, no passphrase in between.
func TestGenerateKeyPair_StoresWithoutAPassphrase(t *testing.T) {
	priv, _, err := GenerateKeyPair("")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(priv, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Errorf("not an OpenSSH private key: %q", priv[:40])
	}
	if _, err := ssh.ParsePrivateKey([]byte(priv)); err != nil {
		t.Errorf("parse: %v", err)
	}
}
