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
			ID:          "mysql_upsert_rows",
			Version:     "1.0",
			Label:       "MySQL",
			Subtitle:    "Upsert rows",
			Color:       "#00758f",
			Icon:        "database",
			BrandLogo:   "/brands/mysql.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "MySQL",
			Tags:        []string{"mysql", "mariadb", "sql", "database", "upsert", "merge", "etl"},
			Description: "Upsert (insert-or-update) rows into a MySQL or MariaDB table. Set the conflict columns — MySQL matches existing rows on those, updating them in place, while new rows get inserted. Reports separate insert vs update counts so downstream notifications can say 'X new + Y updated'.",
			Summary:     "Insert-or-update rows in MySQL/MariaDB via INSERT ... ON DUPLICATE KEY UPDATE, matching existing rows on the conflict columns.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sync customers by email",
					Params: json.RawMessage(`{"table":"customers","conflict_columns":["email"],"column_types":{"email":"VARCHAR(255)"}}`),
					Notes:  "MySQL UNIQUE on TEXT needs a key length, so give the conflict column a sized type like VARCHAR(255). The connection comes from your MySQL connection, set once under Apps.",
				},
				{
					Title:  "Refresh just a few fields on match",
					Params: json.RawMessage(`{"table":"customers","conflict_columns":["email"],"update_columns":["last_seen","plan"],"column_types":{"email":"VARCHAR(255)"}}`),
				},
				{
					Title:  "Insert-if-absent",
					Params: json.RawMessage(`{"table":"signups","conflict_columns":["email"],"update_columns":[],"column_types":{"email":"VARCHAR(255)"}}`),
					Notes:  "Empty update_columns leaves existing rows untouched (MySQL approximation of DO NOTHING).",
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
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "processed", Label: "Rows saved", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table":            {"type":"string","description":"The table to write rows into."},
					"conflict_columns": {"type":"array","items":{"type":"string"},"description":"Columns that identify an existing row; a match updates it instead of inserting a duplicate (e.g. [\"email\"])."},
					"update_columns":   {"type":"array","items":{"type":"string"},"description":"Which columns to overwrite when a row already exists. Empty = leave existing rows untouched (insert-only)."},
					"create_table":     {"type":"boolean","default":true,"description":"Auto-create the table (with a UNIQUE on conflict_columns) when missing. Defaults true."},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"},"description":"Optional: force a column's SQL type. Columns default to text otherwise."},
					"field_mapping":    {"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. conflict_columns refer to the mapped (output) names. Leave empty to write every field."}
				},
				"required":["table","conflict_columns"]
			}`),
		},
		Execute: executeMySQLUpsertRows,
	})
}

// executeMySQLUpsertRows is the MySQL sibling of postgres_upsert_rows.
// Same three-mode update semantics (absent → all non-conflict cols;
// explicit list → just those; explicit [] → effectively no-op), same
// auto-UNIQUE on create_table, same all-or-nothing transaction.
//
// SQL flavor differences vs Postgres:
//
//   - syntax: INSERT ... ON DUPLICATE KEY UPDATE col = VALUES(col)
//     instead of ON CONFLICT (k) DO UPDATE SET col = EXCLUDED.col
//   - quoting: backticks instead of double quotes
//   - placeholders: ? instead of $1/$2/...
//   - "DO NOTHING": MySQL has no direct equivalent. INSERT IGNORE
//     swallows ALL errors, which is too blunt. We approximate by
//     setting the conflict column to itself (a write that's a no-op)
//     when update_columns is empty — same observable behavior as DO
//     NOTHING for the typical case.
func executeMySQLUpsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	return runUpsert(ctx, job, mysqlDialect{}, sqlConn{db: db}, quoteIdentBacktick(table), ri, conflictCols, updateCols, updateColsExplicit)
}
