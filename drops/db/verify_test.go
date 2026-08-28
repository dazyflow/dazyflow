// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// TestVerifyPostgresRejectsGarbage is the heart of the feature: a value that
// isn't a usable Postgres connection must NOT verify (the old behaviour let
// any saved string read as "Connected").
func TestVerifyPostgresRejectsGarbage(t *testing.T) {
	cases := map[string]string{
		"empty":       "",
		"not a dsn":   "whatever you want",
		"unreachable": "postgres://u:p@127.0.0.1:1/nope?sslmode=disable&connect_timeout=2",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
			defer cancel()
			if err := verifyPostgres(ctx, map[string]string{"dsn": dsn}); err == nil {
				t.Fatalf("verifyPostgres(%q) = nil, want error", dsn)
			}
		})
	}
}

func TestVerifyMySQLRejectsGarbage(t *testing.T) {
	for name, dsn := range map[string]string{
		"empty":      "",
		"unparsable": "this is not a mysql dsn",
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if err := verifyMySQL(ctx, map[string]string{"dsn": dsn}); err == nil {
				t.Fatalf("verifyMySQL(%q) = nil, want error", dsn)
			}
		})
	}
}

// TestVerifyPostgresLive confirms a real, reachable DSN verifies. Skipped
// unless DZ_TEST_PG_DSN points at a live Postgres (the bundled dev one works:
// postgres://dazyflow:dazyflow@localhost:5432/dazyflow?sslmode=disable).
func TestVerifyPostgresLive(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("DZ_TEST_PG_DSN"))
	if dsn == "" {
		t.Skip("set DZ_TEST_PG_DSN to a live Postgres DSN to run the live check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := verifyPostgres(ctx, map[string]string{"dsn": dsn}); err != nil {
		t.Fatalf("verifyPostgres(live) = %v, want nil", err)
	}
}

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
