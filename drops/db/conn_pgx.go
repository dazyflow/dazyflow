// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// conn_pgx.go adapts a pgxpool.Pool (the Postgres backend) to the conn
// interface in dialect.go. pgx is a native driver, not database/sql:
// values come back already Go-typed via rows.Values(), column names via
// FieldDescriptions(), and a transaction's Rollback is a no-op once
// Committed, so the deferred rollback covers every error path without an
// explicit call.

type pgxConn struct {
	pool *pgxpool.Pool
}

func (c pgxConn) exec(ctx context.Context, query string) error {
	_, err := c.pool.Exec(ctx, query)
	return err
}

func (c pgxConn) query(ctx context.Context, query string, args []any, limit int) ([]string, []map[string]any, error) {
	rows, err := c.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query: %v", err)
	}
	defer rows.Close()

	// Column names come from FieldDescriptions, captured once before we
	// start iterating so we can map values back to names per row without
	// re-fetching metadata.
	fields := rows.FieldDescriptions()
	columns := make([]string, len(fields))
	for i, f := range fields {
		columns[i] = string(f.Name)
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, nil, fmt.Errorf("scan row %d: %v", len(out), err)
		}
		rec := make(map[string]any, len(columns))
		for i, col := range columns {
			rec[col] = vals[i]
		}
		var stop bool
		out, stop, err = queryGuard(out, rec, limit)
		if err != nil {
			return nil, nil, err
		}
		if stop {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate: %v", err)
	}
	return columns, out, nil
}

func (c pgxConn) execBatch(ctx context.Context, stmtSQL string, headers []string, rows []map[string]any, verb string) (int, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op if already committed

	count := 0
	for i, row := range rows {
		if _, err := tx.Exec(ctx, stmtSQL, bindArgs(headers, row)...); err != nil {
			return 0, fmt.Errorf("%s row %d: %w", verb, i, err)
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}
