// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"strings"
	"testing"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestVerifyPostgres_InvalidConnString covers the ParseConfig-error branch:
// a syntactically broken DSN fails before any connect attempt.
func TestVerifyPostgres_InvalidConnString(t *testing.T) {
	err := verifyPostgres(context.Background(), map[string]string{"dsn": "postgres://%zz"})
	if err == nil || !strings.Contains(err.Error(), "invalid connection string") {
		t.Fatalf("err = %v, want invalid-connection-string", err)
	}
}

// TestVerifyPostgres_Empty covers the empty-DSN guard.
func TestVerifyPostgres_Empty(t *testing.T) {
	if err := verifyPostgres(context.Background(), map[string]string{"dsn": "  "}); err == nil {
		t.Fatal("want error for empty DSN")
	}
}

// TestVerifyMySQL_Empty covers the empty-DSN guard.
func TestVerifyMySQL_Empty(t *testing.T) {
	if err := verifyMySQL(context.Background(), map[string]string{"dsn": ""}); err == nil {
		t.Fatal("want error for empty DSN")
	}
}

// TestVerifyMySQL_SSRFHostBlocked covers the CheckDialHost branch for a TCP
// DSN. With private egress turned OFF, a loopback host is rejected before any
// connection attempt.
func TestVerifyMySQL_SSRFHostBlocked(t *testing.T) {
	hfnet.SetAllowPrivateEgress(false)
	defer hfnet.SetAllowPrivateEgress(true)
	err := verifyMySQL(context.Background(), map[string]string{
		"dsn": "user:pass@tcp(127.0.0.1:3306)/db",
	})
	if err == nil || !strings.Contains(err.Error(), "ssrf_blocked") {
		t.Fatalf("err = %v, want ssrf_blocked", err)
	}
}

// TestVerifyMySQL_Unreachable covers the PingContext-error branch: a parseable
// TCP DSN to a port nothing listens on fails at ping (private egress is on for
// the test process, so the host check passes and the dial is attempted).
func TestVerifyMySQL_Unreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	err := verifyMySQL(ctx, map[string]string{
		"dsn": "user:pass@tcp(127.0.0.1:1)/db?timeout=2s",
	})
	if err == nil || !strings.Contains(err.Error(), "could not connect") {
		t.Fatalf("err = %v, want could-not-connect", err)
	}
}
