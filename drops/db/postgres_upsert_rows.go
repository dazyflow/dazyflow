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
			ID:          "postgres_upsert_rows",
			Version:     "1.0",
			Label:       "Postgres",
			Subtitle:    "Upsert rows",
			Color:       "#336791",
			Icon:        "database",
			BrandLogo:   "/brands/postgres.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "Postgres",
			Tags:        []string{"postgres", "postgresql", "sql", "database", "upsert", "merge", "etl"},
			Description: "Upsert (insert-or-update) rows into a Postgres table. Set the conflict columns — Postgres matches existing rows on those, updating them in place, while new rows get inserted. Pick which columns get updated on a match if you want to preserve some existing values.",
			Summary:     "Insert-or-update rows in Postgres via INSERT ... ON CONFLICT, matching existing rows on the conflict columns.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sync customers by email",
					Params: json.RawMessage(`{"table":"customers","conflict_columns":["email"]}`),
					Notes:  "When update_columns is omitted, every non-conflict column is overwritten from the incoming row. The connection comes from your Postgres connection, set once under Apps.",
				},
				{
					Title:  "Refresh just a few fields on match",
					Params: json.RawMessage(`{"schema":"crm","table":"customers","conflict_columns":["email"],"update_columns":["last_seen","plan"]}`),
				},
				{
					Title:  "Insert-if-absent (DO NOTHING)",
					Params: json.RawMessage(`{"table":"events","conflict_columns":["event_id"],"update_columns":[]}`),
					Notes:  "An empty update_columns becomes ON CONFLICT DO NOTHING — handy for idempotent event ingestion.",
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
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "processed", Label: "Rows processed", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"schema":           {"type":"string","default":"public"},
					"table":            {"type":"string"},
					"conflict_columns": {"type":"array","items":{"type":"string"}},
					"update_columns":   {"type":"array","items":{"type":"string"}},
					"create_table":     {"type":"boolean","default":true,"description":"Auto-create the table (with a UNIQUE on conflict_columns) when missing. Defaults true."},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"}},
					"field_mapping":    {"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. conflict_columns refer to the mapped (output) names. Leave empty to write every field."}
				},
				"required":["table","conflict_columns"]
			}`),
		},
		Execute: executePostgresUpsertRows,
	})
}

// executePostgresUpsertRows runs INSERT ... ON CONFLICT (...) DO UPDATE
// for each input row, in one transaction. Like the insert drop, the
// whole batch is atomic — partial upserts violate downstream contracts.
//
// Conflict semantics: Postgres needs a unique index/constraint covering
// the conflict_columns to match on. When create_table=true we add a
// UNIQUE constraint on those columns at create time; for existing
// tables the user is responsible for the index.
//
// Update set: by default every non-conflict column is overwritten from
// the new row (the EXCLUDED pseudo-table). When update_columns is set,
// only those columns are written — useful when some columns should be
// "first-write-wins" rather than always overwritten. An empty
// update_columns becomes ON CONFLICT DO NOTHING, which is a useful
// "insert if missing" pattern.
func executePostgresUpsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	conflictCols, updateCols, updateColsExplicit, errRes := parseConflictUpdateCols(job)
	if errRes != nil {
		return *errRes, nil
	}

	ri, errRes := parseRowsInput(job)
	if errRes != nil {
		return *errRes, nil
	}

	if errRes := checkConflictInHeaders(job, conflictCols, ri.headers); errRes != nil {
		return *errRes, nil
	}

	pool, err := defaultPGRegistry.pgPool(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	qualified := fmt.Sprintf("%s.%s", quoteIdent(schema), quoteIdent(table))
	return runUpsert(ctx, job, postgresDialect{}, pgxConn{pool: pool}, qualified, ri, conflictCols, updateCols, updateColsExplicit)
}
