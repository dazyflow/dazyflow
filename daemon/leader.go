// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgLeader is single-leader election over a Postgres session-level
// advisory lock. Exactly one dzd in a cluster holds the lock at a time;
// that node is "the leader" and is the only one allowed to fire the cron
// /poll scheduler. If the leader dies, its connection drops, Postgres
// releases the lock, and a follower takes it over on its next attempt.
//
// Why advisory locks: zero schema, automatic release on connection loss
// (no lease-expiry bookkeeping), and `pg_try_advisory_lock` is
// non-blocking so followers poll cheaply.
type PgLeader struct {
	pool   *pgxpool.Pool
	key    int64
	logger *log.Logger

	mu       sync.RWMutex
	isLeader bool
}

// SchedulerLockKey is the advisory-lock key the scheduler leader uses.
// Any constant works as long as every dzd in the cluster agrees on it;
// pick something unlikely to collide with app-level advisory locks.
const SchedulerLockKey int64 = 0x4841_5A59_5F53_4348 // "DAZY_SCH"

func NewPgLeader(pool *pgxpool.Pool, key int64) *PgLeader {
	return &PgLeader{
		pool:   pool,
		key:    key,
		logger: log.New(log.Writer(), "leader: ", log.LstdFlags),
	}
}

// IsLeader reports whether this instance currently holds the lock.
func (l *PgLeader) IsLeader() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isLeader
}

func (l *PgLeader) set(v bool) {
	l.mu.Lock()
	changed := l.isLeader != v
	l.isLeader = v
	l.mu.Unlock()
	if changed {
		if v {
			l.logger.Print("acquired scheduler leadership")
		} else {
			l.logger.Print("lost scheduler leadership")
		}
	}
}

// Run maintains leadership until ctx is cancelled. It grabs a dedicated
// connection, tries the advisory lock, and — if held — keeps the
// connection alive (the lock lives as long as the session) while pinging
// to detect a drop. Followers retry on an interval so they take over
// after a leader failure.
func (l *PgLeader) Run(ctx context.Context) {
	const (
		retryInterval = 5 * time.Second
		pingInterval  = 5 * time.Second
	)
	for ctx.Err() == nil {
		if err := l.acquireAndHold(ctx, pingInterval); err != nil && ctx.Err() == nil {
			l.set(false)
			l.logger.Printf("leader loop: %v", err)
		}
		l.set(false)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryInterval):
		}
	}
}

func (l *PgLeader) acquireAndHold(ctx context.Context, pingInterval time.Duration) error {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release() // releasing the session drops the advisory lock

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", l.key).Scan(&got); err != nil {
		return err
	}
	if !got {
		// Someone else leads. Stay a follower; the caller retries.
		return nil
	}
	l.set(true)
	// Hold the lock: keep the session alive until ctx ends or the
	// connection dies (which releases the lock and lets a peer win).
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := conn.Ping(ctx); err != nil {
				return err // lost the connection → lost leadership
			}
		}
	}
}
