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
			ID:          "postgres_insert_rows",
			Version:     "1.0",
			Label:       "Postgres",
			Subtitle:    "Insert rows",
			Color:       "#336791",
			Icon:        "database",
			BrandLogo:   "/brands/postgres.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "Postgres",
			Tags:        []string{"postgres", "postgresql", "sql", "database", "insert", "etl"},
			Description: "Insert rows into a Postgres table. Accepts rows from Sheets, Excel, or any transform step — the shape is interchangeable across these steps, so no extra mapping needed.",
			Summary:     "Batch-insert rows into a Postgres table inside one transaction; auto-creates the table from headers when missing.",
			Examples: []core.ParamsExample{
				{
					Title:  "Load Excel rows into a new table",
					Params: json.RawMessage(`{"table":"signups"}`),
					Notes:  "schema defaults to public and create_table defaults to true. The connection comes from your Postgres connection, set once under Apps.",
				},
				{
					Title:  "Append into a typed schema",
					Params: json.RawMessage(`{"schema":"sales","table":"orders","create_table":false,"column_types":{"id":"bigint","created_at":"timestamptz"}}`),
				},
			},
			// The connection string is a per-tenant connection, set once under
			// Apps (the same Connect flow as Claude/ntfy) — so the editor shows a
			// "Connect Postgres" affordance instead of a raw DSN field, and the
			// secret never lands in the graph. injectConnectionDefaults fills the
			// unset 'dsn' param from conn.postgres.dsn at run time. No Postgres
			// server? Use the SQLite step instead (zero setup).
			ConnectionFields: []core.ConnectionField{
				{Key: "dsn", Label: "Connection string", Secret: true, Required: true, Placeholder: "postgres://user:pass@host:5432/db"},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Rows saved", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"schema":       {"type":"string","default":"public","description":"The Postgres schema the table lives in."},
					"table":        {"type":"string","description":"The table to write rows into (created automatically if it doesn't exist)."},
					"create_table": {"type":"boolean","default":true,"description":"Auto-create the table from headers when missing. Defaults true."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"},"description":"Optional: force a column's SQL type (e.g. {\"id\":\"bigint\"}). Columns default to text otherwise."},
					"field_mapping":{"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. Leave empty to write every field. For row filtering or defaults, use a Map rows step first."}
				},
				"required":["table"]
			}`),
		},
		Execute: executePostgresInsertRows,
	})
}

// executePostgresInsertRows opens a single Postgres connection,
// batch-inserts the input rows into the named table inside one
// transaction, and reports the count. The whole batch succeeds or
// rolls back — partial loads break the contract downstream nodes
// expect.
//
// Credentials never reach this code as plaintext from the graph JSON:
// the engine resolves ${secret.NAME} placeholders in
// params (see engine/secrets.go) before Execute is invoked, so a DSN
// like "postgres://app:${secret.DB_PROD_PWD}@db/orders" arrives with the
// password already substituted. We hold the resolved DSN only for the
// duration of the call.
//
// Connections: routed through defaultPGRegistry (see conns.go),
// which caches one pgxpool.Pool per (tenant, dsn) and evicts idle
// pools in a lazy sweep. Per-job overhead is now just a pool.Acquire
// + Release rather than a full connect + auth + TLS handshake.
//
// Identifiers (schema, table, column names) are restricted to
// [A-Za-z0-9_] and then wrapped in standard double-quote quoting in
// the generated SQL. That's defense-in-depth: even a future bug in
// the regex wouldn't open a SQL-injection path because the identifier
// would still be quoted in the right place.
func executePostgresInsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	dsn, err := params.String(job.Params, "dsn")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	table, err := params.String(job.Params, "table")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := validateIdent(table); err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("table name %q: %v", table, err)), nil
	}
	schema := "public"
	if s, ok := params.StringOpt(job.Params, "schema"); ok && s != "" {
		schema = s
	}
	if err := validateIdent(schema); err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("schema name %q: %v", schema, err)), nil
	}

	ri, errRes := parseRowsInput(job)
	if errRes != nil {
		return *errRes, nil
	}

	pool, err := defaultPGRegistry.pgPool(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	qualified := fmt.Sprintf("%s.%s", quoteIdent(schema), quoteIdent(table))
	return runInsert(ctx, job, postgresDialect{}, pgxConn{pool: pool}, qualified, ri)
}
