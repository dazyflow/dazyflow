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

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	_ "modernc.org/sqlite"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sqlite_insert_rows",
			Version:        "1.0",
			Label:          "SQLite insert rows",
			Color:          "#0a6abf",
			Icon:           "database",
			BrandLogo:      "/brands/sqlite.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "SQLite",
			Tags:           []string{"sqlite", "sql", "database", "insert", "etl"},
			Description:    "Insert rows into a SQLite table inside the workspace sandbox. Input 'rows' is a list of {column: value} records; optional 'headers' input fixes column order. When create_table=true the table is created from headers (TEXT by default, overridable via column_types).",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Inserted count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":         {"type":"string","format":"workspace-path"},
					"table":        {"type":"string"},
					"create_table": {"type":"boolean"},
					"column_types": {"type":"object","additionalProperties":{"type":"string"}}
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
	path, err := paramString(job.Params, "path")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	table, err := paramString(job.Params, "table")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if !isSafeIdent(table) {
		return errResult(job, "bad_param",
			fmt.Sprintf("table name %q contains characters other than [A-Za-z0-9_]", table)), nil
	}
	if job.WorkspaceRoot == "" {
		return errResult(job, "no_sandbox", "sqlite_insert_rows requires a workspace sandbox"), nil
	}
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return errResult(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return errResult(job, "bad_input", err.Error()), nil
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	for _, h := range headers {
		if !isSafeIdent(h) {
			return errResult(job, "bad_input",
				fmt.Sprintf("column %q contains characters other than [A-Za-z0-9_]", h)), nil
		}
	}

	// Resolve the database path through os.Root so a hostile params
	// payload (or upstream Ref) can't write the SQLite file outside
	// the workspace.
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return errResult(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	// We can't pass the *os.Root handle to database/sql.Open — it
	// needs a filename. Close the root immediately after the safety
	// check; the absolute path is then passed to sqlite directly.
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			root.Close()
			if isSandboxEscape(err) {
				return errResult(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
			}
			return errResult(job, "io", fmt.Sprintf("mkdir: %v", err)), nil
		}
	}
	// Touch the file through the sandbox root so os.Root validates
	// the path before sqlite (which takes an unconstrained filename
	// string) ever sees it.
	probe, probeErr := root.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	root.Close()
	if probeErr != nil {
		if isSandboxEscape(probeErr) {
			return errResult(job, "sandbox_escape", fmt.Sprintf("path %q escapes workspace", path)), nil
		}
		return errResult(job, "io", fmt.Sprintf("open %q: %v", path, probeErr)), nil
	}
	probe.Close()

	absPath := filepath.Join(job.WorkspaceRoot, path)
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		return errResult(job, "db", fmt.Sprintf("open sqlite %q: %v", path, err)), nil
	}
	defer db.Close()

	createTable, _ := paramBool(job.Params, "create_table")
	if createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := ensureTable(db, table, headers, colTypes); err != nil {
			return errResult(job, "db", err.Error()), nil
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
		return errResult(job, "db", err.Error()), nil
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
// unless overridden by column_types. We quote identifiers in case a
// column name shadows a SQL keyword (e.g. "order", "from").
func ensureTable(db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("%q %s", h, t)
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %q (%s)", table, strings.Join(cols, ", "))
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
		cols[i] = fmt.Sprintf("%q", h)
		placeholders[i] = "?"
	}
	stmt, err := tx.Prepare(fmt.Sprintf(
		"INSERT INTO %q (%s) VALUES (%s)",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
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

// isSafeIdent restricts table and column names to [A-Za-z0-9_]. Real
// production SQL uses quoted identifiers and can store any UTF-8, but
// we don't want graph-author-controlled strings to ever become part
// of an identifier we don't fully control — this is a defense-in-depth
// check on top of the quoting we already do.
func isSafeIdent(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
