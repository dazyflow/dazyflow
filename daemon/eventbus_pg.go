// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PgBus is the multi-node Bus. The in-process MemoryBus only fans out to
// subscribers on the same dzd; PgBus lets ANY dzd serve the streaming
// RPC/SSE for a run regardless of which node's worker produced the
// events.
//
// Design — a tiny durable spool + LISTEN/NOTIFY wake:
//
//	Publish:  INSERT the JSON event into bus_events, then pg_notify a
//	          wake on the "dazy_bus" channel.
//	Listener: one dedicated connection LISTENs on "dazy_bus"; on each
//	          wake it drains every row newer than the last it saw and
//	          fans each out to local subscribers for that job. Draining
//	          "id > lastSeen" (not the notify payload) means a missed or
//	          coalesced NOTIFY can't drop an event — the next wake catches
//	          up. All delivery — including same-node — flows through the
//	          listener, so there's no local/remote double-delivery.
//
// NOTIFY's 8 KB payload limit is why the event rides in a table, not the
// notification: a terminal event carries the full GraphResult. Inline
// values round-trip as JSON-generic types across the bus; the JobStore
// stays the type-faithful source of truth (WaitGraph re-fetches from it
// on a closed subscription), so this is a non-issue for correctness.
//
// Spool rows are ephemeral — a background sweep deletes anything older
// than retention. New subscribers don't replay history (lastSeen starts
// at the current max), matching MemoryBus semantics; the gateway already
// reconciles the "already finished before I subscribed" race by
// re-reading the JobRecord.
type PgBus struct {
	pool      *pgxpool.Pool
	logger    *log.Logger
	retention time.Duration

	local localSubscribers

	// lastSeen and seen are touched ONLY by the single listener goroutine
	// (drainNew); no lock needed. lastSeen is the high-water id fanned out.
	// seen dedupes the trailing re-scan window so an event whose row committed
	// out of BIGSERIAL order (a lower id committing after we advanced past it)
	// is still delivered, without re-delivering rows we already fanned. Bounded
	// to ~pgBusReScanWindow ids.
	lastSeen int64
	seen     map[int64]struct{}
}

// pgBusReScanWindow is how far below the high-water mark drainNew re-scans each
// pass. A BIGSERIAL id is assigned at INSERT but only visible at COMMIT, so a
// row can commit with a lower id than one already drained; re-scanning a
// trailing window (deduped via `seen`) catches it. Publishes are single-
// statement (microsecond) transactions, so the number of ids that can commit
// between one publish's INSERT and COMMIT is tiny — 256 is generous headroom.
// Beyond the window the JobStore re-read self-heal is the backstop (terminal/
// node state is durable), so this strictly improves on never re-scanning.
const pgBusReScanWindow = 256

const pgBusSchema = `
CREATE TABLE IF NOT EXISTS bus_events (
    id         BIGSERIAL PRIMARY KEY,
    job_id     TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

const pgBusChannel = "dazy_bus"

// NewPgBus provisions the spool table, captures the current high-water
// mark (so brand-new subscribers don't get a backlog), and starts the
// listener + sweep goroutines. They stop when ctx is cancelled.
func NewPgBus(ctx context.Context, pool *pgxpool.Pool) (*PgBus, error) {
	if err := applyPgSchema(ctx, pool, pgBusSchema); err != nil {
		return nil, err
	}
	b := &PgBus{
		pool:      pool,
		logger:    log.New(log.Writer(), "bus-pg: ", log.LstdFlags),
		retention: time.Hour,
		seen:      make(map[int64]struct{}),
	}
	var maxID *int64
	if err := pool.QueryRow(ctx, `SELECT max(id) FROM bus_events`).Scan(&maxID); err != nil {
		return nil, err
	}
	if maxID != nil {
		b.lastSeen = *maxID
		// drainNew re-scans a trailing window below lastSeen; seed `seen` with
		// the pre-existing ids in that window so the first drain treats them as
		// already-delivered (new subscribers don't replay history) rather than
		// fanning out stale events. Bounded to pgBusReScanWindow rows.
		rows, err := pool.Query(ctx,
			`SELECT id FROM bus_events WHERE id > $1`, b.lastSeen-pgBusReScanWindow)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			b.seen[id] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	go b.listen(ctx)
	go b.sweep(ctx)
	return b, nil
}

// Publish writes the event to the spool and wakes listeners (this node's
// and every peer's). Errors are logged, not returned — the Bus contract
// is fire-and-forget, same as MemoryBus's non-blocking sends.
func (b *PgBus) Publish(jobID string, ev BusEvent) {
	payload, err := json.Marshal(ev)
	if err != nil {
		b.logger.Printf("marshal event for %s: %v", jobID, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.pool.Exec(ctx,
		`INSERT INTO bus_events (job_id, payload) VALUES ($1, $2)`, jobID, payload); err != nil {
		b.logger.Printf("publish %s: %v", jobID, err)
		return
	}
	// Wake listeners. Payload is the job_id (debug aid); the listener
	// drains by id regardless, so a lost notify is self-healing.
	if _, err := b.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, pgBusChannel, jobID); err != nil {
		b.logger.Printf("notify %s: %v", jobID, err)
	}
}

// Subscribe registers a local channel for a job's events. Identical
// fan-out semantics to MemoryBus (buffered, non-blocking, drop-on-slow).
func (b *PgBus) Subscribe(jobID string) (<-chan BusEvent, func()) {
	return b.local.subscribe(jobID)
}

// listen holds a dedicated connection on the LISTEN channel and drains
// the spool on every wake. Reconnects with backoff on failure; lastSeen
// persists across reconnects so the catch-up drain replays anything
// published during the blip.
func (b *PgBus) listen(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := b.listenOnce(ctx); err != nil && ctx.Err() == nil {
			b.logger.Printf("listener: %v (reconnecting in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

func (b *PgBus) listenOnce(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+pgBusChannel); err != nil {
		return err
	}
	// Catch up on anything published before/while (re)connecting.
	b.drainNew(ctx)
	for {
		if _, err := conn.Conn().WaitForNotification(ctx); err != nil {
			return err
		}
		b.drainNew(ctx)
	}
}

// drainNew reads spool rows and fans each out exactly once. It re-scans a
// trailing window below lastSeen (not just `id > lastSeen`) so an event whose
// row committed out of BIGSERIAL order is still caught; `seen` dedupes rows
// already fanned out in a prior pass so the re-scan never re-delivers.
func (b *PgBus) drainNew(ctx context.Context) {
	floor := b.lastSeen - pgBusReScanWindow
	if floor < 0 {
		floor = 0
	}
	rows, err := b.pool.Query(ctx,
		`SELECT id, job_id, payload FROM bus_events WHERE id > $1 ORDER BY id`, floor)
	if err != nil {
		b.logger.Printf("drain: %v", err)
		return
	}
	defer rows.Close()
	type pending struct {
		id        int64
		jobID     string
		ev        BusEvent
		malformed bool // count toward `seen` (don't re-scan) but don't fan out
	}
	// Collect the whole pass into a local batch and commit shared state
	// (seen/lastSeen/fan-out) only AFTER the row loop fully succeeds. A
	// mid-loop scan/query error then discards the pass cleanly — nothing is
	// marked seen and lastSeen doesn't advance, so the next wake re-scans and
	// re-delivers (dedupe keeps that safe).
	batch := make([]pending, 0)
	maxID := b.lastSeen
	for rows.Next() {
		var (
			id      int64
			jobID   string
			payload []byte
		)
		if err := rows.Scan(&id, &jobID, &payload); err != nil {
			b.logger.Printf("drain scan: %v", err)
			return
		}
		if _, dup := b.seen[id]; dup {
			continue // already fanned out in a previous pass
		}
		if id > maxID {
			maxID = id
		}
		var ev BusEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			b.logger.Printf("drain unmarshal (id %d): %v", id, err)
			batch = append(batch, pending{id: id, malformed: true})
			continue
		}
		batch = append(batch, pending{id: id, jobID: jobID, ev: ev})
	}
	if err := rows.Err(); err != nil {
		b.logger.Printf("drain rows: %v", err)
		return
	}
	// Commit: mark every scanned id seen, advance the cursor, then prune `seen`
	// of ids now below the re-scan window (never queried again) so it stays
	// bounded. Advance before fanning out so a slow subscriber can't stall the
	// loop.
	for _, p := range batch {
		b.seen[p.id] = struct{}{}
	}
	b.lastSeen = maxID
	newFloor := b.lastSeen - pgBusReScanWindow
	for id := range b.seen {
		if id <= newFloor {
			delete(b.seen, id)
		}
	}
	for _, p := range batch {
		if !p.malformed {
			b.local.fanout(p.jobID, p.ev)
		}
	}
}

// DeleteByTenant removes every spooled event belonging to a tenant's runs.
// bus_events has no tenant column, so it scopes via the jobs table (same
// database). Part of the org/account erasure cascade (Art. 17).
func (b *PgBus) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := b.pool.Exec(ctx,
		`DELETE FROM bus_events WHERE job_id IN (SELECT id FROM jobs WHERE tenant = $1)`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// sweep deletes spooled events past the retention window so the table
// stays small (events are only useful while a run is live + briefly
// after).
func (b *PgBus) sweep(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := b.pool.Exec(c,
				`DELETE FROM bus_events WHERE created_at < now() - $1::interval`,
				b.retention.String())
			cancel()
			if err != nil {
				b.logger.Printf("sweep: %v", err)
			}
		}
	}
}
