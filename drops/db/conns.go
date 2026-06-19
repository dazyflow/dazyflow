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

	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// errInvalidDSN is returned in place of a driver parse error so a
// credential-bearing connection string never surfaces in a user-visible
// run record.
var errInvalidDSN = errors.New("invalid connection string")

// mysqlSSRFNet is the go-sql-driver/mysql network name whose registered
// dialer applies the shared SSRF guard. Routing TCP MySQL DSNs through it
// makes the MySQL drop's egress defense match Postgres's: the guard runs at
// the actual dial, on the RESOLVED IP, so it's resistant to DNS rebinding
// (a hostname that passes a pre-flight check but resolves to a private or
// metadata address at connect time is still refused). database/sql exposes
// no dial hook, but the mysql driver does.
const mysqlSSRFNet = "dazyflow-ssrf-tcp"

// ssrfMySQLDial dials addr over TCP with the shared SSRF control, which
// inspects the resolved IP at dial time and refuses loopback / private /
// link-local / metadata destinations. A nil control (the operator opted
// into private egress) degrades to a plain dial — same posture as the
// Postgres path.
func ssrfMySQLDial(ctx context.Context, addr string) (stdnet.Conn, error) {
	d := &stdnet.Dialer{Control: hfnet.SSRFDialControl()}
	return d.DialContext(ctx, "tcp", addr)
}

// registerMySQLSSRFDialer installs ssrfMySQLDial under mysqlSSRFNet exactly
// once. RegisterDialContext mutates a process-global map in the driver, so
// it must not run per-connection.
var registerMySQLSSRFDialer = sync.OnceFunc(func() {
	mysql.RegisterDialContext(mysqlSSRFNet, ssrfMySQLDial)
})

// Pool reuse for the Postgres drops. Connection setup (TCP + TLS + auth)
// dominates the wall time of a small per-job INSERT/QUERY, so caching
// pools across jobs that hit the same database is a real win — but
// only if the cache doesn't leak file descriptors when graphs stop
// using a DB.
//
// We don't pool SQLite. Opening a SQLite file is microseconds (no
// network, no auth), and the bigger concern is write-lock contention,
// which pooling makes worse, not better.
//
// Isolation: pools are keyed by (tenant, dsn). Different tenants
// never share a connection, even when pointed at the same DSN — this
// matches the broader "tenant separation by default" stance of the
// daemon. The dsn itself is already credential-bearing post-secret
// resolution, so it's not safe to log or surface in errors verbatim;
// the key is hashed for any internal telemetry. (Not done in v1, but
// the design supports it.)

// dbConnKey uniquely identifies a pooled connection — a pgx pool or a
// database/sql handle. Two jobs with the same key share the connection;
// different tenants are isolated even when they happen to point at the same
// DSN. The shape is SQL-agnostic, so both registries below key on it.
type dbConnKey struct {
	tenant string
	dsn    string
}

// pgEntry pairs a live pool with the timestamp of its last use, so
// the lazy sweep can evict pools whose graphs have gone idle.
type pgEntry struct {
	pool    *pgxpool.Pool
	lastUse time.Time
}

// pgPoolRegistry caches one pool per (tenant, dsn). Idle pools are
// closed in a lazy sweep triggered on Get — no background goroutine,
// which keeps the package usable from tests (no goroutine leak to
// detect) and from short-lived CLI invocations.
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

// defaultPGRegistry is the registry the Postgres drops use. Module-
// level state is unusual in this codebase but the alternative —
// threading a registry through every drop — would mean changing the
// NativeDrop contract for a feature only the db package cares about.
// Tests substitute their own registry via the package-private helpers
// where needed.
var defaultPGRegistry = newPGPoolRegistry(15*time.Minute, 1*time.Minute)

// pgPool returns a pool for (tenant, dsn), creating one on first
// request. The pool is alive until either (a) the sweep evicts it
// after `idle` of inactivity, or (b) the process exits. Callers MUST
// NOT close the returned pool — that's the registry's job.
func (r *pgPoolRegistry) pgPool(ctx context.Context, tenant, dsn string) (*pgxpool.Pool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Opportunistic sweep — runs at most once per sweepGap so a busy
	// graph doesn't pay the eviction scan on every job.
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
		// pgx's parse error can echo the DSN — which carries the password —
		// and this error surfaces verbatim in a user-visible run record. Return
		// a generic message; the host/db naming in genuine connect errors below
		// is safe, the credential-bearing DSN is not.
		return nil, errInvalidDSN
	}
	// SSRF guard: a tenant supplies the DSN, so without this they could point
	// a Postgres drop at an internal host — including dzd's own control-plane
	// database — and probe/connect. Install the shared dial guard (post-DNS,
	// rebinding-resistant); it's a no-op when the operator opted into private
	// egress.
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

// sweepLocked closes pools that haven't been touched in `idle`. The
// caller must hold r.mu. We avoid range-and-delete pitfalls by
// collecting victims first and deleting after the range — Go's spec
// allows in-range delete on maps but the resulting iteration order
// is unspecified, which I'd rather not rely on.
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

// closeAll shuts every pool down. Intended for tests; daemon shutdown
// doesn't bother because process exit closes everything anyway.
// Nil-tolerant so tests that inject zero-value entries don't panic.
func (r *pgPoolRegistry) closeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.pools {
		if e.pool != nil {
			e.pool.Close()
		}
		delete(r.pools, k)
	}
}

// --- *sql.DB registry (used by MySQL drops) -------------------------
//
// database/sql's *sql.DB is itself a connection pool, so the natural
// unit to cache is the DB handle, not individual connections. The
// shape mirrors pgPoolRegistry — separate types kept for type safety;
// generalizing to one generic registry would save ~70 lines but adds
// cognitive load that isn't justified at two pool types.

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
	// driverName lets us configure the registry for any database/sql
	// driver. Today: "mysql". Future SQLite-via-pool would slot in
	// with driverName="sqlite".
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

// defaultMySQLRegistry is the registry the mysql_* drops use. Same
// 15-minute idle / 1-minute sweep-gap defaults as the pg registry.
var defaultMySQLRegistry = newSQLDBRegistry("mysql", 15*time.Minute, 1*time.Minute)

// sqlDB returns a *sql.DB for (tenant, dsn), creating one on first
// request. Like pgPool: callers MUST NOT Close the returned handle —
// that's the registry's job. sql.Open is lazy (no connection until
// first use), so the Ping below is what actually validates the DSN
// reaches a live server. We Ping so a bad DSN errors at registration
// time instead of much later inside the first query.
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

	// SSRF pre-flight: the tenant supplies the DSN. database/sql has no dial
	// hook, so parse the host out of the DSN and refuse private/loopback
	// targets before connecting (no-op when the operator opted into private
	// egress). Only TCP MySQL DSNs are checked; unix sockets aren't network.
	if r.driverName == "mysql" {
		cfg, perr := mysql.ParseDSN(dsn)
		if perr != nil {
			// As with pgx above: the parse error can echo the DSN/password.
			// sql.Open would re-parse and leak it too, so reject here generically.
			return nil, errInvalidDSN
		}
		if cfg.Net == "tcp" {
			// Fast pre-flight: reject an obviously-private/loopback host up
			// front with a clear error (the common misconfig). No-op when the
			// operator opted into private egress.
			if err := hfnet.CheckDialHost(cfg.Addr); err != nil {
				return nil, err
			}
			// Authoritative guard: route the connection through a dialer that
			// re-checks the RESOLVED IP at dial time, closing the DNS-rebinding
			// window the pre-flight alone leaves open (a host can pass the check
			// above, then rebind to 169.254.169.254 before the driver connects).
			// Mirrors the Postgres DialFunc path.
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
