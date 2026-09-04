// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
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

	// pending buffers publishes until the writer flushes them as one statement.
	// See Publish.
	pendingMu sync.Mutex
	pending   []pendingBusEvent
	wake      chan struct{}

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

// Publishing is batched, because each event was its own transaction and a
// transaction is a commit — the one thing on this path that waits for disk. A
// step publishes two or three events, and measured against the queue's own
// writes the bus was costing about a third of execution throughput.
//
// The window is short enough to be invisible in a live stream and long enough
// that a busy fleet collapses many events into one commit.
const (
	pgBusFlushEvery = 20 * time.Millisecond
	// pgBusMaxBatch bounds one statement; beyond it the writer flushes early.
	pgBusMaxBatch = 256
	// pgBusMaxPending bounds the buffer if the database stalls. Publishing is
	// already best-effort — the JobStore is the source of truth and a
	// subscriber re-reads it — so shedding the oldest beats unbounded growth.
	pgBusMaxPending = 20_000
)

type pendingBusEvent struct {
	jobID   string
	payload []byte
}

const pgBusSchema = `
CREATE TABLE IF NOT EXISTS bus_events (
    id         BIGSERIAL PRIMARY KEY,
    job_id     TEXT NOT NULL,
    payload    JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The retention sweep's predicate. Without it the five-minute sweep scans the
-- whole spool.
CREATE INDEX IF NOT EXISTS bus_events_created_idx ON bus_events (created_at);
-- The drain's predicate: the events of the runs THIS replica is watching,
-- newer than its cursor. Leading with job_id because the filter is the
-- selective half — a replica watching a handful of runs must not read the
-- spool of every run in the fleet.
CREATE INDEX IF NOT EXISTS bus_events_job_idx ON bus_events (job_id, id);
`

const pgBusChannel = "dazy_bus"

// NewPgBus provisions the spool table, captures the current high-water
// mark (so brand-new subscribers don't get a backlog), and starts the
// listener + sweep goroutines. They stop when ctx is cancelled.
func NewPgBus(ctx context.Context, pool *pgxpool.Pool) (*PgBus, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgBusSchema); err != nil {
		return nil, err
	}
	b := &PgBus{
		pool:      pool,
		logger:    log.New(log.Writer(), "bus-pg: ", log.LstdFlags),
		retention: time.Hour,
		seen:      make(map[int64]struct{}),
		wake:      make(chan struct{}, 1),
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
	go b.writer(ctx)
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
	b.pendingMu.Lock()
	if len(b.pending) >= pgBusMaxPending {
		// The database is not keeping up. Shed the oldest rather than grow
		// without bound; a subscriber's authority is the JobStore, which it
		// re-reads, and this path has always been best-effort.
		b.pending = b.pending[1:]
	}
	b.pending = append(b.pending, pendingBusEvent{jobID: jobID, payload: payload})
	n := len(b.pending)
	b.pendingMu.Unlock()
	if n >= pgBusMaxBatch {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
}

// Flush writes anything buffered, for a caller that must see its own publishes
// on the far side — the erasure cascade, and tests.
func (b *PgBus) Flush(ctx context.Context) { b.flush(ctx) }

// writer drains the publish buffer into one statement per flush.
func (b *PgBus) writer(ctx context.Context) {
	t := time.NewTicker(pgBusFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			b.flush(context.WithoutCancel(ctx)) // one last pass for what is buffered
			return
		case <-t.C:
		case <-b.wake:
		}
		b.flush(ctx)
	}
}

// flush writes the buffered events as a single insert, and notifies for each
// inside the same transaction — so a batch is one commit and every row still
// carries its own wake.
func (b *PgBus) flush(ctx context.Context) {
	b.pendingMu.Lock()
	batch := b.pending
	if len(batch) > pgBusMaxBatch {
		batch, b.pending = batch[:pgBusMaxBatch], batch[pgBusMaxBatch:]
	} else {
		b.pending = nil
	}
	b.pendingMu.Unlock()
	if len(batch) == 0 {
		return
	}
	jobIDs := make([]string, len(batch))
	payloads := make([][]byte, len(batch))
	for i, e := range batch {
		jobIDs[i], payloads[i] = e.jobID, e.payload
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := b.pool.Exec(writeCtx,
		`WITH ins AS (
		     INSERT INTO bus_events (job_id, payload)
		     SELECT * FROM unnest($1::text[], $2::jsonb[]) RETURNING id, job_id
		 )
		 SELECT pg_notify($3, ins.id || ':' || ins.job_id) FROM ins`,
		jobIDs, payloads, pgBusChannel); err != nil {
		b.logger.Printf("publish batch of %d: %v", len(batch), err)
	}
}

// busNotice is what a notification carries: the spool id and the run it belongs
// to. Malformed (or empty, from an older publisher) reports ok=false, and the
// listener falls back to draining, which is always correct.
func parseBusNotice(payload string) (id int64, jobID string, ok bool) {
	sep := strings.IndexByte(payload, ':')
	if sep <= 0 {
		return 0, "", false
	}
	n, err := strconv.ParseInt(payload[:sep], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, payload[sep+1:], true
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
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		// The cheap path, and the common one: an event for a run nobody here is
		// watching. There is nothing to deliver, so the spool is not read at
		// all — the cursor simply moves past it.
		//
		// Safe precisely BECAUSE there is no subscriber: skipping an event we
		// owe nobody costs nothing, and a subscriber that appears later is only
		// owed what comes after it. A notification that does not parse (or an
		// older publisher's) falls through to the drain, which is always
		// correct.
		if id, jobID, ok := parseBusNotice(n.Payload); ok && !b.local.has(jobID) {
			b.skipTo(id)
			continue
		}
		b.drainNew(ctx)
	}
}

// skipTo advances the cursor past an event this replica has no subscriber for.
// Touched only by the listener goroutine, like lastSeen and seen.
func (b *PgBus) skipTo(id int64) {
	// Marked seen as well as skipped. drainNew re-scans a window BELOW the
	// cursor to catch rows that commit out of order, and `seen` is what stops
	// that window re-delivering. An id skipped without being recorded falls
	// straight back into it — so a later subscriber to the same run was handed
	// the backlog it should never have had.
	b.seen[id] = struct{}{}
	if id <= b.lastSeen {
		return
	}
	b.lastSeen = id
	floor := b.lastSeen - pgBusReScanWindow
	for seen := range b.seen {
		if seen <= floor {
			delete(b.seen, seen)
		}
	}
}

// drainNew reads spool rows and fans each out exactly once. It re-scans a
// trailing window below lastSeen (not just `id > lastSeen`) so an event whose
// row committed out of BIGSERIAL order is still caught; `seen` dedupes rows
// already fanned out in a prior pass so the re-scan never re-delivers.
func (b *PgBus) drainNew(ctx context.Context) {
	// Only the runs with a live local subscriber. Every replica used to read
	// every event in the fleet and hand almost all of them to nobody: most runs
	// are watched by no one, and a watched one by a single replica.
	watching := b.local.jobIDs()
	if len(watching) == 0 {
		return
	}
	floor := b.lastSeen - pgBusReScanWindow
	if floor < 0 {
		floor = 0
	}
	rows, err := b.pool.Query(ctx,
		`SELECT id, job_id, payload FROM bus_events
		  WHERE id > $1 AND job_id = ANY($2) ORDER BY id`, floor, watching)
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
	// Publishing is buffered, so anything still in hand would be written AFTER
	// this delete and leave the erased org's events on disk. Flush first, then
	// erase what is there. (The cascade cancels the org's active runs before
	// reaching here, so nothing should be producing events by now anyway.)
	b.flush(ctx)
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
