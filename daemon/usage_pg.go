package daemon

import (
	"context"
	"time"

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
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant, period)
);
`

func NewPgUsageStore(ctx context.Context, pool *pgxpool.Pool) (*PgUsageStore, error) {
	if _, err := pool.Exec(ctx, pgUsageSchema); err != nil {
		return nil, err
	}
	return &PgUsageStore{pool: pool}, nil
}

func (s *PgUsageStore) add(ctx context.Context, tenant string, runs, nodes int, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_counters (tenant, period, graph_runs, node_executions)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant, period) DO UPDATE SET
			graph_runs      = usage_counters.graph_runs + EXCLUDED.graph_runs,
			node_executions = usage_counters.node_executions + EXCLUDED.node_executions,
			updated_at      = now()`,
		tenant, usagePeriod(now), runs, nodes)
	return err
}

func (s *PgUsageStore) AddRun(ctx context.Context, tenant string, now time.Time) error {
	return s.add(ctx, tenant, 1, 0, now)
}

func (s *PgUsageStore) AddNodeExecutions(ctx context.Context, tenant string, n int, now time.Time) error {
	return s.add(ctx, tenant, 0, n, now)
}

func (s *PgUsageStore) Usage(ctx context.Context, tenant string, months int) ([]UsageCounters, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT period, graph_runs, node_executions
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
		if err := rows.Scan(&c.Period, &c.GraphRuns, &c.NodeExecutions); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
