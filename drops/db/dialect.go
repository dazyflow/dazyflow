// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
)

// errTooManyRows is the sentinel the conn query implementations return
// when an uncapped result set exceeds the shared row ceiling. runQuery*
// maps it back to the "too_many_rows" error code the query drops
// reported before the consolidation (other db errors map to "db").
var errTooManyRows = errors.New("too many rows")

// dialect.go holds the SQL-flavor abstraction the three database
// backends (SQLite, Postgres, MySQL) share. Each backend's query /
// insert / upsert drops once carried a near-identical skeleton —
// connect, optionally CREATE TABLE, run the batch in one transaction,
// report the count — differing only in:
//
//   - how a connection is obtained (sqlite file via os.Root, pgx pool,
//     database/sql handle),
//   - placeholder syntax (? vs $N),
//   - identifier quoting (" vs `),
//   - the conflict/duplicate-key upsert clause.
//
// A dialect captures the SQL differences; a conn abstracts the two
// driver families (pgx vs database/sql) behind a single Exec/Query/tx
// surface. The executeQuery / executeInsert / executeUpsert functions
// below hold the shared skeleton so each backend file is just a dialect
// plus its connection wiring.

// dialect is the per-backend SQL flavor. The generic execute* functions
// build every statement through it, so SQL stays byte-identical to the
// hand-written per-backend code it replaced.
type dialect interface {
	// quote returns ident wrapped in the dialect's identifier quoting,
	// with the embedded quote char doubled to escape it.
	quote(ident string) string
	// placeholder returns the bind marker for the i-th value (1-based):
	// "?" for SQLite/MySQL, "$i" for Postgres.
	placeholder(i int) string
	// upsertClause renders the trailing ON CONFLICT / ON DUPLICATE KEY
	// clause for an INSERT, given the already-quoted statement context.
	// conflictCols and updateCols are raw (unquoted) identifiers.
	upsertClause(conflictCols, updateCols []string) string
}

// placeholders builds the comma-joined bind-marker list for n columns.
func placeholders(d dialect, n int) string {
	ps := make([]string, n)
	for i := range ps {
		ps[i] = d.placeholder(i + 1)
	}
	return strings.Join(ps, ", ")
}

// quoteAll quotes every identifier in names.
func quoteAll(d dialect, names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = d.quote(n)
	}
	return out
}

// insertSQL renders "INSERT INTO <table> (<cols>) VALUES (<ph>)" with an
// optional trailing clause (the upsert tail, empty for a plain insert).
// table is already qualified+quoted by the caller.
func insertSQL(d dialect, table string, headers []string, tail string) string {
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table, strings.Join(quoteAll(d, headers), ", "), placeholders(d, len(headers)))
	if tail != "" {
		stmt += " " + tail
	}
	return stmt
}

// createTableSQL renders CREATE TABLE IF NOT EXISTS sized to headers,
// defaulting each column to TEXT unless column_types overrides it, with
// an optional trailing UNIQUE(conflictCols) constraint. table is already
// qualified+quoted by the caller.
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

// sqliteEnsureTable issues a CREATE TABLE IF NOT EXISTS sized to
// headers against a raw *sql.DB. The dialect-driven runInsert /
// runUpsert path doesn't need it, but the Collections store calls it
// directly (it manages its own schema-evolution dance around the
// create) so it stays as a small standalone helper.
func sqliteEnsureTable(db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	stmt := createTableSQL(sqliteDialect{}, quoteIdent(table), headers, colTypes, nil)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// conn abstracts the two driver families. SQLite and MySQL wrap a
// *sql.DB; Postgres wraps a *pgxpool.Pool. The generic execute*
// functions only need: run one statement, run a SELECT and collect
// rows, and run a batch of bound statements in a single transaction.
type conn interface {
	// exec runs a single statement with no result rows (CREATE TABLE).
	exec(ctx context.Context, sql string) error
	// query runs sql with args and returns the column names and every
	// row as a {column: value} map, applying the limit / row-ceiling
	// guard shared by the three query drops. limit==0 means no
	// user-imposed cap (the ceiling still applies).
	query(ctx context.Context, sql string, args []any, limit int) (cols []string, rows []map[string]any, err error)
	// execBatch runs stmt once per row inside one transaction, binding
	// the row's values in header order; the whole batch commits or rolls
	// back. Returns the number of rows processed.
	execBatch(ctx context.Context, stmt string, headers []string, rows []map[string]any, verb string) (int, error)
}

// bindArgs pulls a row's values in header order. A missing/absent key
// binds nil, which both drivers map to SQL NULL.
func bindArgs(headers []string, row map[string]any) []any {
	args := make([]any, len(headers))
	for j, h := range headers {
		args[j] = row[h]
	}
	return args
}

// queryGuard appends rec to out, enforcing the user limit and the shared
// row ceiling. It returns the updated slice, whether iteration should
// stop (user limit reached), and an error when the ceiling is exceeded.
func queryGuard(out []map[string]any, rec map[string]any, limit int) ([]map[string]any, bool, error) {
	out = append(out, rec)
	if limit > 0 && len(out) >= limit {
		return out, true, nil
	}
	// limit==0 means "no user-imposed cap" — but the whole result set is
	// buffered in memory, so an unbounded SELECT would OOM the daemon.
	// Fail fast at the shared row ceiling rather than letting it grow.
	if len(out) > limits.MaxRows() {
		return out, false, errTooManyRows
	}
	return out, false, nil
}

// --- shared drop skeletons --------------------------------------------

// queryParams is the parsed common input of the three query drops.
type queryParams struct {
	sql   string
	args  []any
	limit int
}

// parseQueryParams reads the sql / params / limit inputs shared by all
// three query drops. A non-nil *core.Result is the caller's verbatim
// error reply.
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

// queryResult builds the shared rows+columns OK Result.
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

// runQuery is the shared body of every query drop once the connection is
// in hand: parse params, run the SELECT through the conn, emit the
// result. The caller supplies the connection (each backend opens it
// differently — sqlite via sandbox probe, pg/mysql via the registries).
func runQuery(ctx context.Context, job core.Job, c conn) (core.Result, error) {
	qp, errRes := parseQueryParams(job)
	if errRes != nil {
		return *errRes, nil
	}
	return runQueryParsed(ctx, job, c, qp)
}

// runQueryParsed is runQuery for callers that validated the params
// earlier (sqlite, which checks them before its sandbox probe so a bad
// query fails before a missing-file error).
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

// insertedResult / processedResult build the count Results the
// insert/upsert drops emit. The port name is the only difference.
func countResult(job core.Job, port string, n int) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			port: {MIME: "application/json", Inline: n},
		},
	}
}

// runInsert is the shared body of every insert drop once the connection
// and qualified+quoted table name are in hand: optionally CREATE TABLE,
// then batch-insert in one transaction.
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

// runUpsert is the shared body of every upsert drop once the connection,
// qualified+quoted table name, parsed rows and conflict/update columns
// are in hand: optionally CREATE TABLE (with the UNIQUE constraint),
// then batch-upsert in one transaction.
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
	// Decide the final update set:
	//   - explicit []        → DO NOTHING
	//   - explicit non-empty → use those (validated upstream)
	//   - absent             → derive (headers minus conflict_columns)
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

// shouldCreateTable reads the create_table param, defaulting to true.
func shouldCreateTable(job core.Job) bool {
	create := true
	if v, present := params.Bool(job.Params, "create_table"); present {
		create = v
	}
	return create
}
