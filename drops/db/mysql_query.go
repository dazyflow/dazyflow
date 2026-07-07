// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "mysql_query",
			Version:     "1.0",
			Label:       "MySQL",
			Subtitle:    "Query",
			Color:       "#00758f",
			Icon:        "database",
			BrandLogo:   "/brands/mysql.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "MySQL",
			Tags:        []string{"mysql", "mariadb", "sql", "database", "query", "select"},
			Description: "Run a SELECT against your MySQL or MariaDB database and get rows back. Use ? placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped. Set a row limit to keep large result sets bounded.",
			Summary:     "Run a parameterized SELECT against MySQL/MariaDB and emit rows plus column names as a result set.",
			Examples: []core.ParamsExample{
				{
					Title:  "Recent orders for one customer",
					Params: json.RawMessage(`{"sql":"SELECT id, total FROM orders WHERE customer_id = ? ORDER BY id DESC LIMIT 50","params":[42]}`),
					Notes:  "The connection comes from your MySQL connection, set once under Apps.",
				},
				{
					Title:  "Aggregate with a row cap",
					Params: json.RawMessage(`{"sql":"SELECT status, count(*) AS n FROM orders GROUP BY status","limit":100}`),
					Notes:  "Empty params is fine when the SQL has no ? placeholders.",
				},
			},
			// Per-tenant connection set once under Apps (same Connect flow as
			// Postgres/Claude/ntfy): the editor shows a "Connect MySQL" affordance,
			// the secret never lands in the graph, and injectConnectionDefaults
			// fills the unset 'dsn' param from conn.mysql.dsn at run time.
			ConnectionFields: []core.ConnectionField{
				{Key: "dsn", Label: "Connection string", Secret: true, Required: true, Placeholder: "user:pass@tcp(host:3306)/db"},
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
					"sql":    {"type":"string","title":"SQL","description":"The SELECT query to run. Use ? placeholders for values and supply them under 'Query values'."},
					"params": {"type":"array","items":{},"title":"Query values","description":"Values for the ? placeholders in the SQL, in order."},
					"limit":  {"type":"integer","minimum":1,"title":"Row limit","description":"Optional cap on how many rows to return."}
				},
				"required":["sql"]
			}`),
			Idempotent: true,
		},
		Execute: executeMySQLQuery,
	})
}

// executeMySQLQuery runs a single SELECT and emits the result as the
// shared {column: value}[] shape — same as postgres_query and
// sqlite_query, so the three are wire-compatible end-to-end.
//
// Type handling caveat: the MySQL driver returns []byte for most
// stored types unless the connection has parseTime + the right
// charset/collation set up. Most users get strings back — this is a
// MySQL-driver quirk, not something the drop tries to fight. If you
// want native int64/float64/time.Time, include parseTime=true in the
// DSN and use INTEGER/DECIMAL/DATETIME column types on the source
// table.
func executeMySQLQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dsn, err := params.String(job.Params, "dsn")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	qp, errRes := parseQueryParams(job)
	if errRes != nil {
		return *errRes, nil
	}

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	// The MySQL driver hands back []byte for text/varchar columns by
	// default; sqlConn.bytesToString converts them so JSON consumers
	// downstream see strings rather than base64 blobs.
	return runQueryParsed(ctx, job, sqlConn{db: db, bytesToString: true}, qp)
}
