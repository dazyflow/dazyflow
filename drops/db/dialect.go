// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/limits"
	"github.com/dazyflow/dazyflow/drops/internal/params"
)

// The sentinel a query returns when its result exceeds the row ceiling.
var errTooManyRows = errors.New("too many rows")

// The SQL-flavour abstraction the three database drops share, so a query is
// written once and the per-engine differences live in one place.

type dialect interface {
	quote(ident string) string
	placeholder(i int) string
	upsertClause(conflictCols, updateCols []string) string
}

func placeholders(d dialect, n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = d.placeholder(i + 1)
	}
	return strings.Join(ps, ", ")
}

func quoteAll(d dialect, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = d.quote(n)
	}
	return out
}

func insertSQL(d dialect, table string, headers []string, tail string) string {
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(quoteAll(d, headers), ", "), placeholders(d, len(headers)))
	if tail != "" {
		stmt += " " + tail
	}
	return stmt
}

func createTableSQL(d dialect, table string, headers []string, colTypes map[string]string, uniqueCols []string) string {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("%s %s", d.quote(h), t)
	}
	body := strings.Join(cols, ", ")
	if len(uniqueCols) > 0 {
		body += fmt.Sprintf(", UNIQUE (%s)", strings.Join(quoteAll(d, uniqueCols), ", "))
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", table, body)
}

func sqliteEnsureTable(db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	stmt := createTableSQL(sqliteDialect{}, quoteIdent(table), headers, colTypes, nil)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

type conn interface {
	exec(ctx context.Context, sql string) error
	query(ctx context.Context, sql string, args []any, limit int) (cols []string, rows []map[string]any, err error)
	execBatch(ctx context.Context, stmt string, headers []string, rows []map[string]any, verb string) (int, error)
}

func bindArgs(headers []string, row map[string]any) []any {
	args := make([]any, len(headers))
	for j, h := range headers {
		args[j] = row[h]
	}
	return args
}

func queryGuard(out []map[string]any, rec map[string]any, limit int) ([]map[string]any, bool, error) {
	out = append(out, rec)
	if limit > 0 && len(out) >= limit {
		return out, true, nil
	}
	if len(out) > limits.MaxRows() {
		return out, false, errTooManyRows
	}
	return out, false, nil
}

type queryParams struct {
	sql   string
	args  []any
	limit int
}

func parseQueryParams(job core.Job) (queryParams, *core.Result) {
	sqlText, err := params.String(job.Params, "sql")
	if err != nil {
		r := params.Err(job, "bad_param", err.Error())
		return queryParams{}, &r
	}
	if sqlText == "" {
		r := params.Err(job, "bad_param", "sql is empty")
		return queryParams{}, &r
	}
	var args []any
	if v, ok := job.Params["params"]; ok && v != nil {
		raw, ok := v.([]any)
		if !ok {
			r := params.Err(job, "bad_param", fmt.Sprintf("params: expected array, got %T", v))
			return queryParams{}, &r
		}
		args = raw
	}
	limit := 0
	if n, ok := paramInt(job.Params, "limit"); ok {
		if n < 0 {
			r := params.Err(job, "bad_param", "limit must be >= 0")
			return queryParams{}, &r
		}
		limit = n
	}
	return queryParams{sql: sqlText, args: args, limit: limit}, nil
}

func queryResult(job core.Job, rows []map[string]any, columns []string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: rows},
			"columns": {MIME: "application/json", Inline: columns},
		},
	}
}

func runQueryParsed(ctx context.Context, job core.Job, c conn, qp queryParams) (core.Result, error) {
	cols, rows, err := c.query(ctx, qp.sql, qp.args, qp.limit)
	if err != nil {
		if errors.Is(err, errTooManyRows) {
			return params.Err(job, "too_many_rows",
				fmt.Sprintf("query returned more than the %d-row limit; add a LIMIT clause, set the 'limit' param, or raise DAZYFLOW_MAX_ROWS", limits.MaxRows())), nil
		}
		return params.Err(job, "db", err.Error()), nil
	}
	return queryResult(job, rows, cols), nil
}

func countResult(job core.Job, port string, n int) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			port: {MIME: "application/json", Inline: n},
		},
	}
}

func runInsert(ctx context.Context, job core.Job, d dialect, c conn, table string, ri rowsInput) (core.Result, error) {
	if shouldCreateTable(job) && len(ri.headers) > 0 {
		colTypes, err := parseColumnTypes(job.Params)
		if err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		if err := c.exec(ctx, createTableSQL(d, table, ri.headers, colTypes, nil)); err != nil {
			return params.Err(job, "db", fmt.Sprintf("create table: %v", err)), nil
		}
	}
	if len(ri.rows) == 0 {
		return countResult(job, "inserted", 0), nil
	}
	stmt := insertSQL(d, table, ri.headers, "")
	n, err := c.execBatch(ctx, stmt, ri.headers, ri.rows, "insert")
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return countResult(job, "inserted", n), nil
}

func runUpsert(ctx context.Context, job core.Job, d dialect, c conn, table string, ri rowsInput, conflictCols, updateCols []string, updateColsExplicit bool) (core.Result, error) {
	if shouldCreateTable(job) && len(ri.headers) > 0 {
		colTypes, err := parseColumnTypes(job.Params)
		if err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		if err := c.exec(ctx, createTableSQL(d, table, ri.headers, colTypes, conflictCols)); err != nil {
			return params.Err(job, "db", fmt.Sprintf("create table: %v", err)), nil
		}
	}
	if len(ri.rows) == 0 {
		return countResult(job, "processed", 0), nil
	}
	if !updateColsExplicit {
		updateCols = subtract(ri.headers, conflictCols)
	}
	stmt := insertSQL(d, table, ri.headers, d.upsertClause(conflictCols, updateCols))
	n, err := c.execBatch(ctx, stmt, ri.headers, ri.rows, "upsert")
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return countResult(job, "processed", n), nil
}

func shouldCreateTable(job core.Job) bool {
	create := true
	if v, present := params.Bool(job.Params, "create_table"); present {
		create = v
	}
	return create
}
