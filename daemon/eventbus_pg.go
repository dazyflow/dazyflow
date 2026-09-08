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

// The multi-node Bus: MemoryBus only fans out within one process, so a browser
// streaming a run from one replica would see nothing of a run executing on
// another. Events are spooled to a table and replayed via LISTEN/NOTIFY.
type PgBus struct {
	pool      *pgxpool.Pool
	logger    *log.Logger
	retention time.Duration

	local localSubscribers

	pendingMu sync.Mutex
	pending   []pendingBusEvent
	wake      chan struct{}
	drained   chan struct{}

	// Touched ONLY by the single listener goroutine, so they need no lock.
	lastSeen int64
	seen     map[int64]struct{}
}

// Sequence numbers are assigned before commit, so a row with a lower id can
// become visible after a higher one — the re-scan is what stops that being a
// dropped event.
const pgBusReScanWindow = 256

const (
	pgBusFlushEvery = 20 * time.Millisecond
	pgBusMaxBatch   = 256
	// Publishing must never block a run, so a stalled database drops events instead.
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
		drained:   make(chan struct{}),
	}
	var maxID *int64
	if err := pool.QueryRow(ctx, `SELECT max(id) FROM bus_events`).Scan(&maxID); err != nil {
		return nil, err
	}
	if maxID != nil {
		b.lastSeen = *maxID
		// The seen set is what makes the re-scan idempotent.
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

func (b *PgBus) Publish(jobID string, ev BusEvent) {
	payload, err := json.Marshal(ev)
	if err != nil {
		b.logger.Printf("marshal event for %s: %v", jobID, err)
		return
	}
	b.pendingMu.Lock()
	if len(b.pending) >= pgBusMaxPending {
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

func (b *PgBus) Flush(ctx context.Context) { b.flush(ctx) }

func (b *PgBus) writer(ctx context.Context) {
	defer close(b.drained)
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

func (b *PgBus) Close() {
	select {
	case <-b.drained:
	case <-time.After(2 * time.Second):
	}
}

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

func (b *PgBus) Subscribe(jobID string) (<-chan BusEvent, func()) {
	return b.local.subscribe(jobID)
}

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
	b.drainNew(ctx)
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if id, jobID, ok := parseBusNotice(n.Payload); ok && !b.local.has(jobID) {
			b.skipTo(id)
			continue
		}
		b.drainNew(ctx)
	}
}

func (b *PgBus) skipTo(id int64) {
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

func (b *PgBus) drainNew(ctx context.Context) {
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

func (b *PgBus) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	b.flush(ctx)
	tag, err := b.pool.Exec(ctx,
		`DELETE FROM bus_events WHERE job_id IN (SELECT id FROM jobs WHERE tenant = $1)`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

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
