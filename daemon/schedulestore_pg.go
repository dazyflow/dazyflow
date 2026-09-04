// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dazyflow/dazyflow/daemon/internal/pgstore"
)

// PgScheduleStore is the durable ScheduleStore. Every dzd reads the same set,
// so a schedule authored on one replica is enrolled on the leader without
// either of them touching the other's disk.
type PgScheduleStore struct {
	pool *pgxpool.Pool
}

// entry_key already embeds tenant/workspace/graph_id and is unique per
// enrollment, so it is the natural primary key; the flow index backs the
// per-flow replace and the erasure cascade.
const pgScheduleSchema = `
CREATE TABLE IF NOT EXISTS flow_schedules (
    entry_key        TEXT PRIMARY KEY,
    tenant           TEXT NOT NULL,
    workspace        TEXT NOT NULL,
    graph_id         TEXT NOT NULL,
    spec_key         TEXT NOT NULL,
    cron_expr        TEXT NOT NULL DEFAULT '',
    cron_tz          TEXT NOT NULL DEFAULT '',
    interval_seconds INTEGER NOT NULL DEFAULT 0,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS flow_schedules_flow_idx ON flow_schedules (tenant, workspace, graph_id);
CREATE INDEX IF NOT EXISTS flow_schedules_tenant_idx ON flow_schedules (tenant);
`

func NewPgScheduleStore(ctx context.Context, pool *pgxpool.Pool) (*PgScheduleStore, error) {
	if err := pgstore.ApplySchema(ctx, pool, pgScheduleSchema); err != nil {
		return nil, err
	}
	return &PgScheduleStore{pool: pool}, nil
}

const scheduleColumns = `entry_key, tenant, workspace, graph_id, spec_key, cron_expr, cron_tz, interval_seconds`

func (s *PgScheduleStore) ListSchedules(ctx context.Context) ([]ScheduleSpec, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scheduleColumns+` FROM flow_schedules ORDER BY entry_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScheduleSpec
	for rows.Next() {
		var spec ScheduleSpec
		if err := rows.Scan(&spec.EntryKey, &spec.Tenant, &spec.Workspace, &spec.GraphID,
			&spec.SpecKey, &spec.Cron, &spec.TZ, &spec.IntervalSeconds); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, rows.Err()
}

// ReplaceFlowSchedules swaps one flow's rows in a transaction, so a rescan
// concurrent with a publish sees either the old set or the new one — never a
// flow with half its triggers enrolled.
func (s *PgScheduleStore) ReplaceFlowSchedules(ctx context.Context, tenant, workspace, graphID string, specs []ScheduleSpec) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`DELETE FROM flow_schedules WHERE tenant = $1 AND workspace = $2 AND graph_id = $3`,
		tenant, workspace, graphID); err != nil {
		return err
	}
	for _, spec := range specs {
		if spec.Tenant != tenant || spec.Workspace != workspace || spec.GraphID != graphID {
			return fmt.Errorf("schedule %s does not belong to %s/%s/%s", spec.EntryKey, tenant, workspace, graphID)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO flow_schedules (`+scheduleColumns+`, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`,
			spec.EntryKey, spec.Tenant, spec.Workspace, spec.GraphID,
			spec.SpecKey, spec.Cron, spec.TZ, spec.IntervalSeconds); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *PgScheduleStore) DeleteByTenant(ctx context.Context, tenant string) (int, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM flow_schedules WHERE tenant = $1`, tenant)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// PruneMissingFlows removes rows for flows absent from live, the set of flow
// keys the workspaces actually hold. It is the delete half of a reconcile: a
// flow deleted while its dzd was down leaves rows nothing else will clear.
func (s *PgScheduleStore) PruneMissingFlows(ctx context.Context, live map[string]struct{}) (int, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT tenant, workspace, graph_id FROM flow_schedules`)
	if err != nil {
		return 0, err
	}
	type flow struct{ tenant, workspace, graphID string }
	var stale []flow
	for rows.Next() {
		var f flow
		if err := rows.Scan(&f.tenant, &f.workspace, &f.graphID); err != nil {
			rows.Close()
			return 0, err
		}
		if _, ok := live[flowKey(f.tenant, f.workspace, f.graphID)]; !ok {
			stale = append(stale, f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, f := range stale {
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM flow_schedules WHERE tenant = $1 AND workspace = $2 AND graph_id = $3`,
			f.tenant, f.workspace, f.graphID)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}
