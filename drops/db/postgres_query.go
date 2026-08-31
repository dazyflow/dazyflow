// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "postgres_query",
			Version:     "1.0",
			Label:       "Postgres",
			Subtitle:    "Query",
			Color:       "#336791",
			Icon:        "database",
			BrandLogo:   "/brands/postgres.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "Postgres",
			Tags:        []string{"postgres", "postgresql", "sql", "database", "query", "select"},
			Description: "Run a SELECT against your Postgres database and get rows back. Use $1, $2 placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped. Set a row limit to keep large result sets bounded.",
			Summary:     "Run a parameterized SELECT against Postgres and stream rows back as a result set with typed values.",
			Examples: []core.ParamsExample{
				{
					Title:  "Recent orders for one customer",
					Params: json.RawMessage(`{"sql":"SELECT id, total FROM orders WHERE customer_id = $1 ORDER BY id DESC LIMIT 50","params":[42]}`),
				},
				{
					Title:  "Count by status",
					Params: json.RawMessage(`{"sql":"SELECT status, count(*) FROM orders GROUP BY status"}`),
					Notes:  "Empty params is fine when the SQL has no $N placeholders. The connection comes from your Postgres connection, set once under Apps.",
				},
			},
			// Per-tenant connection (same Connect flow as Claude/ntfy): the editor
			// shows "Connect Postgres" rather than a raw DSN field, and
			// injectConnectionDefaults fills the unset 'dsn' from conn.postgres.dsn
			// at run time. No Postgres server? Use the SQLite step instead.
			ConnectionFields: []core.ConnectionField{
				{Key: "dsn", Label: "Connection string", Secret: true, Required: true, Placeholder: "postgres://user:pass@host:5432/db"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"sql":    {"type":"string","title":"SQL","description":"The SELECT query to run. Use $1, $2 … placeholders for values and supply them under 'Query values'."},
					"params": {"type":"array","items":{},"title":"Query values","description":"Values for the $1, $2 … placeholders in the SQL, in order."},
					"limit":  {"type":"integer","minimum":1,"title":"Row limit","description":"Optional cap on how many rows to return."}
				},
				"required":["sql"]
			}`),
			Idempotent: true,
		},
		Execute: executePostgresQuery,
	})
}

// executePostgresQuery runs a single SELECT (or any read-only statement
// that produces rows) and emits the result as a list of {column: value}
// maps plus a column-name list. The two outputs together feed the same
// row shape downstream nodes already consume from excel_read — so the
// reverse loop (DB → Excel report, DB → AI prompt input) is wire-up
// compatible with the forward one.
//
// Parameterization: the graph author writes "$1, $2" placeholders in
// the SQL and passes a `params` array; pgx binds them with type
// inference. This is the only safe way to combine graph-author input
// with SQL — string concatenation belongs in a transformer node where
// the user knows they're hand-rolling escaping.
//
// Values: pgx returns Go-typed values per Postgres column type
// (int64, float64, string, bool, time.Time, []byte, []any for arrays,
// map[string]any for jsonb). The map[string]any output marshals
// straight to JSON, so consumers downstream see typed values rather
// than the all-strings shape excel_read produces. That's a deliberate
// asymmetry: SQL has types, spreadsheets don't.
//
// Connection lifecycle is the shared registry (see conns.go) — same
// pgxpool.Pool reused across all postgres_* drops for a given
// (tenant, dsn).
func executePostgresQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dsn, err := params.String(job.Params, "dsn")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	qp, errRes := parseQueryParams(job)
	if errRes != nil {
		return *errRes, nil
	}

	pool, err := defaultPGRegistry.pgPool(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	return runQueryParsed(ctx, job, pgxConn{pool: pool}, qp)
}
