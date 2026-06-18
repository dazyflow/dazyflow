package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
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
