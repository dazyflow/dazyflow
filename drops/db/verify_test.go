// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// The heart of the feature: a value that isn't a usable Postgres connection
// must NOT verify (the old behaviour let any saved string read as
// "Connected").
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

func TestVerifyPostgres_InvalidConnString(t *testing.T) {
	err := verifyPostgres(context.Background(), map[string]string{"dsn": "postgres://%zz"})
	if err == nil || !strings.Contains(err.Error(), "invalid connection string") {
		t.Fatalf("err = %v, want invalid-connection-string", err)
	}
}

func TestVerifyPostgres_Empty(t *testing.T) {
	if err := verifyPostgres(context.Background(), map[string]string{"dsn": "  "}); err == nil {
		t.Fatal("want error for empty DSN")
	}
}

func TestVerifyMySQL_Empty(t *testing.T) {
	if err := verifyMySQL(context.Background(), map[string]string{"dsn": ""}); err == nil {
		t.Fatal("want error for empty DSN")
	}
}

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
