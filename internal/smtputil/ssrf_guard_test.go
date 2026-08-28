// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package smtputil

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeSMTP stands in for an internal mail relay / any TCP service an attacker
// could rebind a tenant-supplied SMTP host onto. It speaks just enough of the
// protocol for dial's NewClient + QUIT to succeed.
func fakeSMTP(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				c.Write([]byte("220 relay ESMTP\r\n"))
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						c.Write([]byte("250 relay\r\n"))
					case strings.HasPrefix(line, "DATA"):
						c.Write([]byte("354 end with .\r\n"))
						for {
							l, err := br.ReadString('\n')
							if err != nil {
								return
							}
							if l == ".\r\n" {
								break
							}
						}
						c.Write([]byte("250 queued\r\n"))
					case strings.HasPrefix(line, "QUIT"):
						c.Write([]byte("221 bye\r\n"))
						return
					default:
						c.Write([]byte("250 ok\r\n"))
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// TestVerifyBlocksLoopback guards the SSRF fix: the tenant-facing SMTP path
// (Verify / Send) must refuse to connect to a loopback/private address at dial
// time, so DNS rebinding past the pre-flight CheckDialHost cannot reach
// internal services or exfiltrate the configured AUTH credentials.
func TestVerifyBlocksLoopback(t *testing.T) {
	addr, stop := fakeSMTP(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := Verify(ctx, addr, "127.0.0.1", "none", nil); err == nil {
		t.Fatalf("Verify connected to loopback %s — SSRF dial guard missing", addr)
	} else if !strings.Contains(err.Error(), "ssrf_blocked") {
		t.Fatalf("Verify failed for the wrong reason: %v", err)
	}
}

// TestSendTrustedAllowsLoopback documents that the operator's own Mailer path
// is intentionally exempt: its host comes from trusted instance config and may
// legitimately be an internal/sidecar relay.
func TestSendTrustedAllowsLoopback(t *testing.T) {
	addr, stop := fakeSMTP(t)
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := SendTrusted(ctx, addr, "127.0.0.1", "none", nil,
		"from@example.com", []string{"to@example.com"}, []byte("Subject: t\r\n\r\nbody\r\n")); err != nil {
		t.Fatalf("SendTrusted should reach an internal relay, got: %v", err)
	}
}
