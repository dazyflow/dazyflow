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
			ID:             "sqlite_upsert_rows",
			Version:        "1.0",
			Label:          "SQLite upsert rows",
			Color:          "#0a6abf",
			Icon:           "database",
			BrandLogo:      "/brands/sqlite.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "SQLite",
			Tags:           []string{"sqlite", "sql", "database", "upsert", "merge", "etl"},
			Description:    "Upsert (insert-or-update) rows into a SQLite table in your workspace. Set the conflict columns — SQLite matches existing rows on those, updating them in place, while new rows get inserted. Pick which columns get updated on a match if you want to preserve some existing values.",
			Summary:        "Insert-or-update rows in a workspace-sandboxed SQLite file via INSERT ... ON CONFLICT, matching on the conflict columns.",
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
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "processed", Label: "Processed count", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":             {"type":"string","format":"workspace-path"},
					"table":            {"type":"string"},
					"conflict_columns": {"type":"array","items":{"type":"string"}},
					"update_columns":   {"type":"array","items":{"type":"string"}},
					"create_table":     {"type":"boolean","default":true,"description":"Auto-create the table (with a UNIQUE on conflict_columns) when missing. Defaults true."},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"}}
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
func executeSQLiteUpsertRows(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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

	conflictCols, err := paramStringArray(job.Params, "conflict_columns")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if len(conflictCols) == 0 {
		return params.Err(job, "bad_param", "conflict_columns must list at least one column"), nil
	}
	for _, c := range conflictCols {
		if err := validateIdent(c); err != nil {
			return params.Err(job, "bad_param", fmt.Sprintf("conflict column %q: %v", c, err)), nil
		}
	}

	var updateCols []string
	var updateColsExplicit bool
	if raw, ok := job.Params["update_columns"]; ok {
		updateColsExplicit = true
		uc, err := normalizeStringArray(raw, "update_columns")
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
		updateCols = uc
		for _, c := range updateCols {
			if err := validateIdent(c); err != nil {
				return params.Err(job, "bad_param", fmt.Sprintf("update column %q: %v", c, err)), nil
			}
		}
	}

	rowsRef, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	rows, err := normalizeRows(rowsRef.Inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	var headers []string
	if h, ok := job.Input["headers"]; ok && h.Inline != nil {
		headers, err = normalizeHeaders(h.Inline)
		if err != nil {
			return params.Err(job, "bad_input", err.Error()), nil
		}
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	for _, h := range headers {
		if err := validateIdent(h); err != nil {
			return params.Err(job, "bad_input", fmt.Sprintf("column %q: %v", h, err)), nil
		}
	}
	headerSet := map[string]struct{}{}
	for _, h := range headers {
		headerSet[h] = struct{}{}
	}
	for _, c := range conflictCols {
		if _, ok := headerSet[c]; !ok {
			return params.Err(job, "bad_param",
				fmt.Sprintf("conflict_column %q is not in headers", c)), nil
		}
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

	createTable := true
	if v, present := paramBool(job.Params, "create_table"); present {
		createTable = v
	}
	if createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := sqliteEnsureTableWithUnique(db, table, headers, colTypes, conflictCols); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
	}

	if len(rows) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"processed": {MIME: "application/json", Inline: 0},
			},
		}, nil
	}

	if !updateColsExplicit {
		updateCols = subtract(headers, conflictCols)
	}

	processed, err := sqliteUpsertBatch(db, table, headers, conflictCols, updateCols, rows)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"processed": {MIME: "application/json", Inline: processed},
		},
	}, nil
}

// sqliteEnsureTableWithUnique mirrors the Postgres helper — CREATE
// TABLE IF NOT EXISTS with a UNIQUE on the conflict columns. SQLite
// uses the same syntax for this, so the only real difference is the
// default storage class (TEXT here matches drops/io/excel_read's
// all-strings output).
func sqliteEnsureTableWithUnique(db *sql.DB, table string, headers []string, colTypes map[string]string, conflictCols []string) error {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("%s %s", quoteIdent(h), t)
	}
	uniqueCols := make([]string, len(conflictCols))
	for i, c := range conflictCols {
		uniqueCols[i] = quoteIdent(c)
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s, UNIQUE (%s))",
		quoteIdent(table), strings.Join(cols, ", "), strings.Join(uniqueCols, ", "))
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// sqliteUpsertBatch runs all rows in one transaction. Generated SQL:
//
//	INSERT INTO "t" ("a","b","c")
//	VALUES (?,?,?)
//	ON CONFLICT ("a") DO UPDATE
//	  SET "b" = excluded."b", "c" = excluded."c"
//
// Lowercase `excluded` matches SQLite's convention (Postgres uses
// upper). DO NOTHING substitutes when updateCols is empty.
func sqliteUpsertBatch(db *sql.DB, table string, headers, conflictCols, updateCols []string, rows []map[string]any) (int, error) {
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
	conflictList := make([]string, len(conflictCols))
	for i, c := range conflictCols {
		conflictList[i] = quoteIdent(c)
	}

	var conflictClause string
	if len(updateCols) == 0 {
		conflictClause = fmt.Sprintf("ON CONFLICT (%s) DO NOTHING", strings.Join(conflictList, ", "))
	} else {
		assignments := make([]string, len(updateCols))
		for i, c := range updateCols {
			q := quoteIdent(c)
			assignments[i] = fmt.Sprintf("%s = excluded.%s", q, q)
		}
		conflictClause = fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s",
			strings.Join(conflictList, ", "), strings.Join(assignments, ", "))
	}

	stmt, err := tx.Prepare(fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) %s",
		quoteIdent(table), strings.Join(cols, ", "), strings.Join(placeholders, ", "), conflictClause,
	))
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare upsert: %w", err)
	}
	defer stmt.Close()

	count := 0
	for i, row := range rows {
		args := make([]any, len(headers))
		for j, h := range headers {
			args[j] = row[h]
		}
		if _, err := stmt.Exec(args...); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("upsert row %d: %w", i, err)
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}
