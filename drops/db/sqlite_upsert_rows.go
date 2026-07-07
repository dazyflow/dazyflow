// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
	_ "modernc.org/sqlite"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "sqlite_upsert_rows",
			Version:     "1.0",
			Label:       "SQLite",
			Subtitle:    "Upsert rows",
			Color:       "#0a6abf",
			Icon:        "database",
			BrandLogo:   "/brands/sqlite.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "SQLite",
			Tags:        []string{"sqlite", "sql", "database", "upsert", "merge", "etl"},
			// Modest boost so SQLite outranks the no-setup KV store for
			// "database" too, while plain Insert rows (boost 25) still leads.
			SearchBoost: 10,
			Description: "Upsert (insert-or-update) rows into a SQLite table in your workspace. Set the conflict columns — SQLite matches existing rows on those, updating them in place, while new rows get inserted. Pick which columns get updated on a match if you want to preserve some existing values.",
			Summary:     "Insert-or-update rows in a workspace-sandboxed SQLite file via INSERT ... ON CONFLICT, matching on the conflict columns.",
			Examples: []core.ParamsExample{
				{
					Title:  "Sync customers by email",
					Params: json.RawMessage(`{"path":"data/app.db","table":"customers","conflict_columns":["email"]}`),
					Notes:  "When update_columns is omitted, every non-conflict column is overwritten from the incoming row.",
				},
				{
					Title:  "Refresh just a few fields on match",
					Params: json.RawMessage(`{"path":"data/app.db","table":"customers","conflict_columns":["email"],"update_columns":["last_seen","plan"]}`),
				},
				{
					Title:  "Insert-if-absent (DO NOTHING)",
					Params: json.RawMessage(`{"path":"data/events.db","table":"events","conflict_columns":["event_id"],"update_columns":[]}`),
					Notes:  "An empty update_columns becomes ON CONFLICT DO NOTHING — useful for idempotent event ingestion.",
				},
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
					"path":             {"type":"string","format":"workspace-path","title":"Database file","description":"Path to the SQLite database file in your workspace (created if missing)."},
					"table":            {"type":"string","description":"The table to write rows into."},
					"conflict_columns": {"type":"array","items":{"type":"string"},"description":"Columns that identify an existing row; a match updates it instead of inserting a duplicate (e.g. [\"email\"])."},
					"update_columns":   {"type":"array","items":{"type":"string"},"description":"Which columns to overwrite when a row already exists. Empty = leave existing rows untouched (insert-only)."},
					"create_table":     {"type":"boolean","default":true,"description":"Auto-create the table (with a UNIQUE on conflict_columns) when missing. Defaults true."},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"},"description":"Optional: force a column's SQL type. Columns default to text otherwise."},
					"field_mapping":    {"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. conflict_columns refer to the mapped (output) names. Leave empty to write every field."}
				},
				"required":["path","table","conflict_columns"]
			}`),
		},
		Execute: executeSQLiteUpsertRows,
	})
}

// executeSQLiteUpsertRows is the SQLite sibling of
// postgres_upsert_rows — same three-mode semantics for update_columns
// (absent → all non-conflict cols; explicit list → just those;
// explicit [] → DO NOTHING), same transaction-or-nothing batching,
// same UNIQUE-constraint auto-creation when create_table=true.
//
// SQLite syntax notes vs Postgres:
//   - ? placeholders instead of $1, $2, ...
//   - excluded (lowercase) instead of EXCLUDED
//   - SQLite's ON CONFLICT requires 3.24+; modernc.org/sqlite ships
//     a recent build so this is safe.
func executeSQLiteUpsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := params.String(job.Params, "path")
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
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "sqlite_upsert_rows requires a workspace sandbox"), nil
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

	// Sandbox probe + mkdirs, same pattern as sqlite_insert_rows.
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return params.Err(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			root.Close()
			if isSandboxEscape(err) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}
	probe, probeErr := root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	root.Close()
	if probeErr != nil {
		if isSandboxEscape(probeErr) {
			return params.Err(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
		}
		return params.Err(job, "io", fmt.Sprintf("open %q: %v", path, probeErr)), nil
	}
	probe.Close()

	absPath := filepath.Join(job.WorkspaceRoot, path)
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("open sqlite %q: %v", path, err)), nil
	}
	defer db.Close()

	return runUpsert(ctx, job, sqliteDialect{}, sqlConn{db: db}, quoteIdent(table), ri, conflictCols, updateCols, updateColsExplicit)
}
