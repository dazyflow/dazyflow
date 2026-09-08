// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	stdnet "net"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgxpool"

	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// In place of the driver's parse error, which can echo the DSN's password.
var errInvalidDSN = errors.New("invalid connection string")

// database/sql has no dial hook, so the guard is installed as a named network
// the DSN is rewritten to use.
const mysqlSSRFNet = "dazyflow-ssrf-tcp"

func ssrfMySQLDial(ctx context.Context, addr string) (stdnet.Conn, error) {
	d := &stdnet.Dialer{Control: hfnet.SSRFDialControl()}
	return d.DialContext(ctx, "tcp", addr)
}

// Exactly once: registering twice panics.
var registerMySQLSSRFDialer = sync.OnceFunc(func() {
	mysql.RegisterDialContext(mysqlSSRFNet, ssrfMySQLDial)
})

// Connection setup dominates a short query, and a flow that runs per-row would
// otherwise pay it every time.

type dbConnKey struct {
	tenant string
	dsn    string
}

type pgEntry struct {
	pool    *pgxpool.Pool
	lastUse time.Time
}

// Keyed by (tenant, dsn), so one tenant's pool is never handed to another.
type pgPoolRegistry struct {
	mu        sync.Mutex
	pools     map[dbConnKey]*pgEntry
	idle      time.Duration // pools unused for this long get closed
	sweepGap  time.Duration // minimum interval between sweeps
	lastSweep time.Time
}

func newPGPoolRegistry(idle, sweepGap time.Duration) *pgPoolRegistry {
	return &pgPoolRegistry{
		pools:    map[dbConnKey]*pgEntry{},
		idle:     idle,
		sweepGap: sweepGap,
	}
}

var defaultPGRegistry = newPGPoolRegistry(15*time.Minute, 1*time.Minute)

func (r *pgPoolRegistry) pgPool(ctx context.Context, tenant, dsn string) (*pgxpool.Pool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if time.Since(r.lastSweep) >= r.sweepGap {
		r.sweepLocked(time.Now())
	}

	key := dbConnKey{tenant: tenant, dsn: dsn}
	if e, ok := r.pools[key]; ok {
		e.lastUse = time.Now()
		return e.pool, nil
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errInvalidDSN
	}
	// A tenant supplies the DSN, so without this it could point at localhost.
	if ctrl := hfnet.SSRFDialControl(); ctrl != nil {
		d := &stdnet.Dialer{Control: ctrl}
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (stdnet.Conn, error) {
			return d.DialContext(ctx, network, addr)
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r.pools[key] = &pgEntry{pool: pool, lastUse: time.Now()}
	return pool, nil
}

// Idle pools are closed, or a tenant that ran once holds a connection for ever.
func (r *pgPoolRegistry) sweepLocked(now time.Time) {
	r.lastSweep = now
	var victims []dbConnKey
	for k, e := range r.pools {
		if now.Sub(e.lastUse) > r.idle {
			victims = append(victims, k)
		}
	}
	for _, k := range victims {
		if p := r.pools[k].pool; p != nil {
			p.Close()
		}
		delete(r.pools, k)
	}
}

type sqlDBEntry struct {
	db      *sql.DB
	lastUse time.Time
}

type sqlDBRegistry struct {
	mu        sync.Mutex
	dbs       map[dbConnKey]*sqlDBEntry
	idle      time.Duration
	sweepGap  time.Duration
	lastSweep time.Time
	// So the same registry serves any database/sql driver.
	driverName string
}

func newSQLDBRegistry(driverName string, idle, sweepGap time.Duration) *sqlDBRegistry {
	return &sqlDBRegistry{
		dbs:        map[dbConnKey]*sqlDBEntry{},
		idle:       idle,
		sweepGap:   sweepGap,
		driverName: driverName,
	}
}

var defaultMySQLRegistry = newSQLDBRegistry("mysql", 15*time.Minute, 1*time.Minute)

func (r *sqlDBRegistry) sqlDB(ctx context.Context, tenant, dsn string) (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if time.Since(r.lastSweep) >= r.sweepGap {
		r.sweepLocked(time.Now())
	}

	key := dbConnKey{tenant: tenant, dsn: dsn}
	if e, ok := r.dbs[key]; ok {
		e.lastUse = time.Now()
		return e.db, nil
	}

	// database/sql has no dial hook, so the host is checked before opening.
	if r.driverName == "mysql" {
		cfg, perr := mysql.ParseDSN(dsn)
		if perr != nil {
			return nil, errInvalidDSN
		}
		if cfg.Net == "tcp" {
			if err := hfnet.CheckDialHost(cfg.Addr); err != nil {
				return nil, err
			}
			registerMySQLSSRFDialer()
			cfg.Net = mysqlSSRFNet
			dsn = cfg.FormatDSN()
		}
	}

	db, err := sql.Open(r.driverName, dsn)
	if err != nil {
		return nil, errInvalidDSN
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	r.dbs[key] = &sqlDBEntry{db: db, lastUse: time.Now()}
	return db, nil
}

func (r *sqlDBRegistry) sweepLocked(now time.Time) {
	r.lastSweep = now
	var victims []dbConnKey
	for k, e := range r.dbs {
		if now.Sub(e.lastUse) > r.idle {
			victims = append(victims, k)
		}
	}
	for _, k := range victims {
		if d := r.dbs[k].db; d != nil {
			_ = d.Close()
		}
		delete(r.dbs, k)
	}
}
