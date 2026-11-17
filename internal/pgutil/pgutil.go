// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pgutil holds small pgx helpers shared by the Postgres-backed stores.
package pgutil

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Row is what a scan function reads from: a pgx.Row or the current row of
// pgx.Rows.
type Row interface {
	Scan(dest ...any) error
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// QueryRows runs query and scans every row with scan. The result is never nil,
// so an empty listing encodes as [] rather than null.
func QueryRows[T any](ctx context.Context, q Querier, scan func(Row) (T, error), query string, args ...any) ([]T, error) {
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
