// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgWriteDedupeStore is the shared, Postgres-backed core.WriteDedupeStore. It
// closes the cross-process gap the in-memory engine.memoryWriteDedupe leaves
// open: in a multi-node cluster, a lease reclaim by a DIFFERENT dzd can't see
// the first node's in-memory record and re-fires the non-idempotent write
// (Twilio SMS, Gmail send, Discord/Sheets/Home Assistant). Recording the result
// in a shared row instead lets any node's reclaim find the prior result.
//
// The guarantee is unchanged — at-least-once. Get and the external write are
// not atomic, so two nodes that both Get a miss before either Puts can still
// double-fire (the same narrow window single-node already has between a Get miss
// and Put). What this removes is the LARGER cross-node window where a record
// existed but was invisible to other processes. Put is first-writer-wins
// (ON CONFLICT DO NOTHING): the same job ID reproduces the same write, so the
// earliest recorded result is the right one to replay.
type PgWriteDedupeStore struct {
	pool   *pgxpool.Pool
	ttl    time.Duration
	logger *log.Logger
}

// pgWriteDedupeTTL mirrors the in-memory store's hour: the record only needs to
// outlive the re-execution window (an expired lease is reclaimed within a lease
// duration; crash recovery within minutes), so an hour is generous. A sweep
// prunes expired rows; Get also treats a stale row as absent as a backstop.
const pgWriteDedupeTTL = time.Hour

// pgWriteDedupeSweepInterval is how often expired rows are pruned. The TTL is
// the correctness boundary; the sweep just bounds table growth.
const pgWriteDedupeSweepInterval = 10 * time.Minute

const pgWriteDedupeSchema = `
CREATE TABLE IF NOT EXISTS write_dedupe (
    key        TEXT PRIMARY KEY,
    result     JSONB NOT NULL,
    stored_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS write_dedupe_stored_at_idx ON write_dedupe (stored_at);
`

// EnsurePgWriteDedupeSchema creates the write_dedupe table. Idempotent.
func EnsurePgWriteDedupeSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return applyPgSchema(ctx, pool, pgWriteDedupeSchema)
}

// NewPgWriteDedupeStore creates the schema and starts the background TTL sweep.
func NewPgWriteDedupeStore(ctx context.Context, pool *pgxpool.Pool) (*PgWriteDedupeStore, error) {
	if err := EnsurePgWriteDedupeSchema(ctx, pool); err != nil {
		return nil, err
	}
	s := &PgWriteDedupeStore{
		pool:   pool,
		ttl:    pgWriteDedupeTTL,
		logger: log.New(log.Writer(), "writededupe: ", log.LstdFlags),
	}
	go s.sweepLoop(ctx)
	return s, nil
}

// Get returns the recorded successful result for key, or false. A DB error or a
// stale row both read as absent so the worker re-runs (at-least-once holds).
func (s *PgWriteDedupeStore) Get(ctx context.Context, key string) (core.Result, bool) {
	var (
		blob     []byte
		storedAt time.Time
	)
	err := s.pool.QueryRow(ctx,
		`SELECT result, stored_at FROM write_dedupe WHERE key=$1`, key).Scan(&blob, &storedAt)
	if err != nil {
		return core.Result{}, false
	}
	if time.Since(storedAt) > s.ttl {
		// Stale: drop opportunistically and report absent so a recorded write
		// can't suppress a legitimate re-run forever.
		_, _ = s.pool.Exec(ctx, `DELETE FROM write_dedupe WHERE key=$1`, key)
		return core.Result{}, false
	}
	var result core.Result
	if err := json.Unmarshal(blob, &result); err != nil {
		return core.Result{}, false
	}
	return result, true
}

// Put records a successful result for key. Best-effort and first-writer-wins:
// a failed insert just means a re-execution re-fires, which is the contract.
func (s *PgWriteDedupeStore) Put(ctx context.Context, key string, result core.Result) {
	blob, err := json.Marshal(result)
	if err != nil {
		s.logger.Printf("marshal result for %s: %v", key, err)
		return
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO write_dedupe (key, result) VALUES ($1, $2) ON CONFLICT (key) DO NOTHING`,
		key, blob); err != nil {
		s.logger.Printf("put %s: %v", key, err)
	}
}

func (s *PgWriteDedupeStore) sweepLoop(ctx context.Context) {
	t := time.NewTicker(pgWriteDedupeSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.pool.Exec(ctx,
				`DELETE FROM write_dedupe WHERE stored_at < now() - $1::interval`,
				s.ttl.String()); err != nil {
				s.logger.Printf("sweep: %v", err)
			}
		}
	}
}
