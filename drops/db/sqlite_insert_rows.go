// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db hosts database-connector drops. Today: SQLite. Postgres
// and friends slot in alongside as separate drops sharing the same
// row-input shape (a list of {column: value} records, as emitted by
// excel_read and friends).
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
			ID:          "sqlite_insert_rows",
			Version:     "1.0",
			Label:       "SQLite",
			Subtitle:    "Insert rows",
			Color:       "#0a6abf",
			Icon:        "database",
			BrandLogo:   "/brands/sqlite.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "SQLite",
			Tags:        []string{"sqlite", "sql", "database", "insert", "save", "store", "etl"},
			// Win the "save"/"database" verb over the no-setup KV store
			// (Collections), which matches the same generic terms — SQLite
			// is the canonical zero-config save-to-a-database default.
			SearchBoost: 25,
			Description: "Save rows into a database file kept in your workspace — no server, connection string, or setup needed (this is the easy database; use Postgres/MySQL only if you already have one). The table is auto-created from the row shape by default; flip create_table off if you've already set up a schema you don't want overwritten.",
			Summary:     "Save rows into a workspace database file — no setup; the table is auto-created from the row shape.",
			Examples: []core.ParamsExample{
				{
					Title:  "Save Excel rows to a local database",
					Params: json.RawMessage(`{"path":"data/signups.db","table":"signups"}`),
					Notes:  "The path is workspace-relative; the file (and any parent dirs) are created on first insert.",
				},
				{
					Title:  "Append into a pre-existing schema",
					Params: json.RawMessage(`{"path":"data/app.db","table":"orders","create_table":false,"column_types":{"id":"INTEGER","created_at":"DATETIME"}}`),
				},
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
					"path":         {"type":"string","format":"workspace-path","title":"Database file","default":"data/app.db","description":"Database file kept in your workspace. Created automatically on first save."},
					"table":        {"type":"string","title":"Table","default":"rows","description":"Which table to save into. Created from the row columns when create_table=true (the default)."},
					"create_table": {"type":"boolean","default":true,"description":"Auto-create the table from the supplied headers if it doesn't exist. Set false to fail loudly when the table is missing."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"},"description":"Override per-column type (e.g. {\"age\":\"INTEGER\",\"created_at\":\"DATETIME\"}). Defaults to TEXT for every header."},
					"field_mapping":{"type":"object","additionalProperties":{"type":"string"},"title":"Column mapping","description":"Optional. Choose which incoming fields to write and name their columns — {incoming field: column name}. Only listed fields are written (others dropped); blank a column name to skip a field. Leave empty to write every field. For row filtering or defaults, use a Map rows step first."}
				},
				"required":["path","table"]
			}`),
		},
		Execute: executeSQLiteInsertRows,
	})
}

// executeSQLiteInsertRows opens (or creates) a SQLite file under the
// workspace sandbox and batch-inserts the input rows into the named
// table inside a single transaction. The whole batch succeeds or rolls
// back — partial inserts are worse than no inserts for an ETL step.
//
// Sandboxing: SQLite's connection string is a filesystem path, so we
// route it through the same os.Root discipline as file_read/excel_read
// — the path is workspace-relative, "../" or absolute paths are
// rejected. modernc.org/sqlite opens via stdlib database/sql, which
// just takes a filename; we pre-resolve it through os.Root to fail
// fast on escape attempts.
//
// Schema: when create_table=true and the table is missing, we CREATE
// TABLE from the resolved header list. Columns default to TEXT; the
// column_types param overrides per column ("age":"INTEGER",
// "created_at":"DATETIME"). Existing tables are left untouched —
// schema migrations are out of scope here.
//
// Quotas: SQLite grows the file in pages; we don't pre-check against
// the tenant quota because the per-INSERT delta isn't knowable up
// front. A daily quota sweep is the right place for that, not this
// drop.
func executeSQLiteInsertRows(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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
		return params.Err(job, "no_sandbox", "sqlite_insert_rows requires a workspace sandbox"), nil
	}
	ri, errRes := parseRowsInput(job)
	if errRes != nil {
		return *errRes, nil
	}

	// Resolve the database path through os.Root so a hostile params
	// payload (or upstream Ref) can't write the SQLite file outside
	// the workspace.
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return params.Err(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	// We can't pass the *os.Root handle to database/sql.Open — it
	// needs a filename. Close the root immediately after the safety
	// check; the absolute path is then passed to sqlite directly.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			root.Close()
			if isSandboxEscape(err) {
				return params.Err(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
			}
			return params.Err(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}
	// Touch the file through the sandbox root so os.Root validates
	// the path before sqlite (which takes an unconstrained filename
	// string) ever sees it.
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

	return runInsert(ctx, job, sqliteDialect{}, sqlConn{db: db}, quoteIdent(table), ri)
}
