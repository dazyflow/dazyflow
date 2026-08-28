// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"fmt"
)

// conn_sql.go adapts a database/sql handle (used by the SQLite and
// MySQL backends — modernc.org/sqlite and go-sql-driver/mysql both ride
// on database/sql) to the conn interface in dialect.go. The single
// behavioral knob is bytesToString: the MySQL driver hands back []byte
// for text/varchar columns by default, which we convert so JSON
// consumers downstream see strings rather than base64 blobs. SQLite
// returns native Go types and leaves it off.

type sqlConn struct {
	db            *sql.DB
	bytesToString bool
}

func (c sqlConn) exec(ctx context.Context, query string) error {
	_, err := c.db.ExecContext(ctx, query)
	return err
}

func (c sqlConn) query(ctx context.Context, query string, args []any, limit int) ([]string, []map[string]any, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query: %v", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("columns: %v", err)
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		// database/sql scans by reference, so we need a slice of
		// pointers-into-vals to receive the row, then read vals back
		// into a map.
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, fmt.Errorf("scan row %d: %v", len(out), err)
		}
		rec := make(map[string]any, len(columns))
		for i, col := range columns {
			if c.bytesToString {
				if b, ok := vals[i].([]byte); ok {
					rec[col] = string(b)
					continue
				}
			}
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

func (c sqlConn) execBatch(ctx context.Context, stmtSQL string, headers []string, rows []map[string]any, verb string) (int, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, stmtSQL)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare %s: %w", verb, err)
	}
	defer stmt.Close()

	count := 0
	for i, row := range rows {
		if _, err := stmt.ExecContext(ctx, bindArgs(headers, row)...); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("%s row %d: %w", verb, i, err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}
