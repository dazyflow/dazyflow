// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	stdnet "net"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// Connection verification for the SQL integrations. Registered here so the
// Apps page can test a DSN before storing it — a bad DSN fails at connect
// time with a readable error instead of silently breaking every flow that
// references the connection (and showing a misleading "Connected").
//
// These are one-off connections, deliberately NOT routed through the pooled
// registries (pgPool / sqlDB): a verify is a transient check against
// possibly-invalid credentials, so caching a pool keyed on a DSN that may
// not even work would only pollute the cache. The same SSRF guards the pools
// install apply here — a tenant supplies the DSN, so verification must not
// become an internal-network probe.
func init() {
	engine.RegisterConnectionVerifier("Postgres", verifyPostgres)
	engine.RegisterConnectionVerifier("MySQL", verifyMySQL)
}

// verifyPostgres opens a short-lived pool from the candidate DSN and pings
// it. The caller's ctx carries the timeout. Errors are wrapped so the user
// sees "could not connect: …" — pgx error text names the host/db but not the
// password, so it's safe to surface.
func verifyPostgres(ctx context.Context, conn map[string]string) error {
	dsn := strings.TrimSpace(conn["dsn"])
	if dsn == "" {
		return errors.New("connection string is empty")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("invalid connection string: %w", err)
	}
	// Same SSRF dial guard the pooled path installs (post-DNS,
	// rebinding-resistant); a no-op when the operator opted into private egress.
	if ctrl := hfnet.SSRFDialControl(); ctrl != nil {
		d := &stdnet.Dialer{Control: ctrl}
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (stdnet.Conn, error) {
			return d.DialContext(ctx, network, addr)
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("could not connect: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("could not connect: %w", err)
	}
	return nil
}

// verifyMySQL opens a one-off *sql.DB from the candidate DSN and pings it,
// mirroring the SSRF pre-flight the pooled MySQL path uses (database/sql has
// no dial hook, so the host is checked before connecting).
func verifyMySQL(ctx context.Context, conn map[string]string) error {
	dsn := strings.TrimSpace(conn["dsn"])
	if dsn == "" {
		return errors.New("connection string is empty")
	}
	cfg, perr := mysql.ParseDSN(dsn)
	if perr != nil {
		return fmt.Errorf("invalid connection string: %w", perr)
	}
	if cfg.Net == "tcp" {
		if err := hfnet.CheckDialHost(cfg.Addr); err != nil {
			return err
		}
	}
	connDB, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("invalid connection string: %w", err)
	}
	defer connDB.Close()
	if err := connDB.PingContext(ctx); err != nil {
		return fmt.Errorf("could not connect: %w", err)
	}
	return nil
}
