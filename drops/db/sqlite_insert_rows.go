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
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	_ "modernc.org/sqlite"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sqlite_insert_rows",
			Version:        "1.0",
			Label:          "SQLite",
			Subtitle:       "Insert rows",
			Color:          "#0a6abf",
			Icon:           "database",
			BrandLogo:      "/brands/sqlite.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "SQLite",
			Tags:           []string{"sqlite", "sql", "database", "insert", "etl"},
			Description:    "Insert rows into a SQLite table in your workspace. The table is auto-created from the row shape by default — flip create_table off if you've already set up the schema with indexes or constraints you don't want overwritten.",
			Summary:        "Batch-insert rows into a workspace-sandboxed SQLite file inside one transaction; auto-creates the table from headers.",
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
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Rows inserted", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":         {"type":"string","format":"workspace-path","title":"Database file","description":"SQLite file path inside the workspace sandbox. Created on first insert."},
					"table":        {"type":"string","description":"Target table. Created from headers when create_table=true (the default)."},
					"create_table": {"type":"boolean","default":true,"description":"Auto-create the table from the supplied headers if it doesn't exist. Set false to fail loudly when the table is missing."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"},"description":"Override per-column type (e.g. {\"age\":\"INTEGER\",\"created_at\":\"DATETIME\"}). Defaults to TEXT for every header."}
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
func executeSQLiteInsertRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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
	rows, headers := ri.rows, ri.headers

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

	// create_table defaults to true (matches the schema). The user
	// only opts out when they've pre-created the table with custom
	// indexes / constraints they don't want overwritten. params.Bool
	// returns (val, present) so we can distinguish "unset" from
	// "explicit false."
	createTable := true
	if v, present := params.Bool(job.Params, "create_table"); present {
		createTable = v
	}
	if createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := ensureTable(db, table, headers, colTypes); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
	}

	if len(rows) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"inserted": {MIME: "application/json", Inline: 0},
			},
		}, nil
	}

	inserted, err := insertBatch(db, table, headers, rows)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"inserted": {MIME: "application/json", Inline: inserted},
		},
	}, nil
}

// ensureTable issues a CREATE TABLE IF NOT EXISTS sized to headers.
// Column types default to TEXT (SQLite's universal storage class)
// unless overridden by column_types. Identifiers are wrapped in
// proper SQL double-quotes (NOT Go's %q, which uses C-style escape
// sequences SQLite doesn't understand) so non-ASCII names like
// "FÖRETAG" and SQL keywords like "order" both round-trip safely.
func ensureTable(db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("%s %s", quoteIdent(h), t)
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", quoteIdent(table), strings.Join(cols, ", "))
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// insertBatch runs all rows in one transaction. Per-row failure
// rolls the whole batch back — half a load is worse than no load
// when the next step in the pipeline assumes the table is consistent.
func insertBatch(db *sql.DB, table string, headers []string, rows []map[string]any) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	cols := make([]string, len(headers))
	placeholders := make([]string, len(headers))
	for i, h := range headers {
		cols[i] = quoteIdent(h)
		placeholders[i] = "?"
	}
	stmt, err := tx.Prepare(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		quoteIdent(table), strings.Join(cols, ", "), strings.Join(placeholders, ", "),
	))
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for i, row := range rows {
		args := make([]any, len(headers))
		for j, h := range headers {
			args[j] = row[h] // nil → NULL via database/sql
		}
		if _, err := stmt.Exec(args...); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("insert row %d: %w", i, err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}
