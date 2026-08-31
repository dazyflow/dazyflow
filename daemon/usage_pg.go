// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PgUsageStore is the durable, multi-replica UsageStore. One row per
// (tenant, month); increments are atomic INSERT … ON CONFLICT upserts so
// concurrent workers across dzd instances never lose counts.
type PgUsageStore struct {
	pool *pgxpool.Pool
}

const pgUsageSchema = `
CREATE TABLE IF NOT EXISTS usage_counters (
    tenant          TEXT NOT NULL,
    period          TEXT NOT NULL, -- UTC calendar month, "YYYY-MM"
    graph_runs      BIGINT NOT NULL DEFAULT 0,
    node_executions BIGINT NOT NULL DEFAULT 0,
    skipped_runs    BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, period)
);
-- Backfill the column on tables created before it existed.
ALTER TABLE usage_counters
    ADD COLUMN IF NOT EXISTS skipped_runs BIGINT NOT NULL DEFAULT 0;
`

func NewPgUsageStore(ctx context.Context, pool *pgxpool.Pool) (*PgUsageStore, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgUsageSchema); err != nil {
		return nil, err
	}
	return &PgUsageStore{pool: pool}, nil
}

func (s *PgUsageStore) add(ctx context.Context, tenant string, runs, nodes, skipped int, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_counters (tenant, period, graph_runs, node_executions, skipped_runs)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant, period) DO UPDATE SET
			graph_runs      = usage_counters.graph_runs + EXCLUDED.graph_runs,
			node_executions = usage_counters.node_executions + EXCLUDED.node_executions,
			skipped_runs    = usage_counters.skipped_runs + EXCLUDED.skipped_runs,
			updated_at      = now()`,
		tenant, usagePeriod(now), runs, nodes, skipped)
	return err
}

func (s *PgUsageStore) AddRun(ctx context.Context, tenant string, now time.Time) error {
	return s.add(ctx, tenant, 1, 0, 0, now)
}

// AddRunIfUnder atomically increments graph_runs for the month iff it is still
// below limit, in a single statement so concurrent submissions can't all read
// an under-limit count and over-admit. A brand-new month-row inserts count=1
// (limit is always >= 1 here). When the row exists and is already at the cap,
// the DO UPDATE … WHERE matches nothing, RETURNING yields no row (ErrNoRows),
// and we report not-admitted without counting.
func (s *PgUsageStore) AddRunIfUnder(ctx context.Context, tenant string, now time.Time, limit int) (bool, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO usage_counters (tenant, period, graph_runs)
		VALUES ($1, $2, 1)
		ON CONFLICT (tenant, period) DO UPDATE SET
			graph_runs = usage_counters.graph_runs + 1,
			updated_at = now()
		WHERE usage_counters.graph_runs < $3
		RETURNING graph_runs`,
		tenant, usagePeriod(now), limit).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // at/over the cap — nothing updated
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *PgUsageStore) AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error {
	return s.add(ctx, tenant, 0, n, 0, now)
}

func (s *PgUsageStore) AddSkippedRun(ctx context.Context, tenant string, now time.Time) error {
	return s.add(ctx, tenant, 0, 0, 1, now)
}

// DeleteByTenant removes every monthly counter row for a tenant, returning the
// count. The erasure cascade's hook (GDPR Art. 17) — nothing else ever deletes
// from this table, so without it an erased org's usage history is permanent.
func (s *PgUsageStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM usage_counters WHERE tenant=$1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (s *PgUsageStore) Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT period, graph_runs, node_executions, skipped_runs
		FROM usage_counters
		WHERE tenant = $1
		ORDER BY period DESC
		LIMIT $2`,
		tenant, months)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageCounters
	for rows.Next() {
		var c UsageCounters
		if err := rows.Scan(&c.Period, &c.GraphRuns, &c.NodeExecutions, &c.SkippedRuns); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
