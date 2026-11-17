// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package pgutil holds small pgx helpers shared by the Postgres-backed stores.
package pgutil

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Scanner is the Scan half of pgx.Row and pgx.Rows, so one scan function
// serves both QueryRow and a rows loop.
type Scanner interface {
	Scan(dest ...any) error
}

// Querier is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// QueryRows runs query and scans every row with scan. An empty result is a
// non-nil empty slice. Errors are returned unwrapped.
func QueryRows[T any](ctx context.Context, q Querier, scan func(Scanner) (T, error), query string, args ...any) ([]T, error) {
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
