package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "mysql_upsert_rows",
			Version:        "1.0",
			Label:          "MySQL upsert rows",
			Color:          "#00758f",
			Icon:           "database",
			BrandLogo:      "/brands/mysql.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "MySQL",
			Tags:           []string{"mysql", "mariadb", "sql", "database", "upsert", "merge", "etl"},
			Description:    "INSERT ... ON DUPLICATE KEY UPDATE against MySQL. 'conflict_columns' lists the UNIQUE/PK columns MySQL matches on; 'update_columns' (optional) restricts which columns get updated — default is all non-conflict columns. With create_table=true the table is created with a UNIQUE constraint on the conflict columns.",
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
					"dsn":              {"type":"string"},
					"table":            {"type":"string"},
					"conflict_columns": {"type":"array","items":{"type":"string"}},
					"update_columns":   {"type":"array","items":{"type":"string"}},
					"create_table":     {"type":"boolean"},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["dsn","table","conflict_columns"]
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
	dsn, err := paramString(job.Params, "dsn")
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

	conflictCols, err := paramStringArray(job.Params, "conflict_columns")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if len(conflictCols) == 0 {
		return errResult(job, "bad_param", "conflict_columns must list at least one column"), nil
	}
	for _, c := range conflictCols {
		if !isSafeIdent(c) {
			return errResult(job, "bad_param",
				fmt.Sprintf("conflict column %q contains characters other than [A-Za-z0-9_]", c)), nil
		}
	}

	var updateCols []string
	var updateColsExplicit bool
	if raw, ok := job.Params["update_columns"]; ok {
		updateColsExplicit = true
		uc, err := normalizeStringArray(raw, "update_columns")
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
		updateCols = uc
		for _, c := range updateCols {
			if !isSafeIdent(c) {
				return errResult(job, "bad_param",
					fmt.Sprintf("update column %q contains characters other than [A-Za-z0-9_]", c)), nil
			}
		}
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
	headerSet := map[string]struct{}{}
	for _, h := range headers {
		headerSet[h] = struct{}{}
	}
	for _, c := range conflictCols {
		if _, ok := headerSet[c]; !ok {
			return errResult(job, "bad_param",
				fmt.Sprintf("conflict_column %q is not in headers", c)), nil
		}
	}

	db, err := defaultMySQLRegistry.sqlDB(ctx, job.Tenant, dsn)
	if err != nil {
		return errResult(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	if createTable, _ := paramBool(job.Params, "create_table"); createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := mysqlEnsureTableWithUnique(ctx, db, table, headers, colTypes, conflictCols); err != nil {
			return errResult(job, "db", err.Error()), nil
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

	processed, err := mysqlUpsertBatch(ctx, db, table, headers, conflictCols, updateCols, rows)
	if err != nil {
		return errResult(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"processed": {MIME: "application/json", Inline: processed},
		},
	}, nil
}

// mysqlEnsureTableWithUnique mirrors pgEnsureTableWithUnique — CREATE
// TABLE IF NOT EXISTS with a UNIQUE on the conflict columns. MySQL
// supports `UNIQUE (col1, col2)` inline the same way Postgres does.
// Column types default to TEXT to match the Excel-comes-as-strings
// reality; override per-column via column_types.
func mysqlEnsureTableWithUnique(ctx context.Context, db *sql.DB, table string, headers []string, colTypes map[string]string, conflictCols []string) error {
	cols := make([]string, len(headers))
	for i, h := range headers {
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		cols[i] = fmt.Sprintf("`%s` %s", h, t)
	}
	// MySQL's UNIQUE on TEXT columns requires a key length: `UNIQUE
	// (col(255))`. To keep the user from having to know that, we
	// build the constraint without a length and rely on the user to
	// pick a sized type (VARCHAR(N) / CHAR(N) / INT) for conflict
	// columns via column_types — which is the right shape anyway,
	// since TEXT primary keys are an anti-pattern.
	uniqueCols := make([]string, len(conflictCols))
	for i, c := range conflictCols {
		uniqueCols[i] = fmt.Sprintf("`%s`", c)
	}
	stmt := fmt.Sprintf("CREATE TABLE IF NOT EXISTS `%s` (%s, UNIQUE (%s))",
		table, strings.Join(cols, ", "), strings.Join(uniqueCols, ", "))
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// mysqlUpsertBatch generates:
//
//	INSERT INTO `t` (`a`, `b`, `c`)
//	VALUES (?, ?, ?)
//	ON DUPLICATE KEY UPDATE
//	  `b` = VALUES(`b`), `c` = VALUES(`c`)
//
// VALUES(col) refers to "the value I tried to insert for col" —
// MySQL's equivalent of Postgres's EXCLUDED.col. It's been around
// forever; the newer row-alias syntax (INSERT ... AS new ON
// DUPLICATE KEY UPDATE col = new.col) requires MySQL 8.0.20+, so we
// stick with VALUES(...) for broader compatibility.
//
// "DO NOTHING" equivalent: when updateCols is empty, we set the
// first conflict column to itself (`col = VALUES(col)`). This is a
// no-op semantically (the existing value stays) while still using
// the standard ON DUPLICATE KEY UPDATE path — no INSERT IGNORE,
// which would swallow unrelated errors like data-type mismatches.
func mysqlUpsertBatch(ctx context.Context, db *sql.DB, table string, headers, conflictCols, updateCols []string, rows []map[string]any) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}

	cols := make([]string, len(headers))
	placeholders := make([]string, len(headers))
	for i, h := range headers {
		cols[i] = fmt.Sprintf("`%s`", h)
		placeholders[i] = "?"
	}

	effectiveUpdates := updateCols
	if len(effectiveUpdates) == 0 {
		// No-op write to preserve existing semantics (vs INSERT
		// IGNORE which would mask data errors).
		effectiveUpdates = []string{conflictCols[0]}
	}
	assignments := make([]string, len(effectiveUpdates))
	for i, c := range effectiveUpdates {
		assignments[i] = fmt.Sprintf("`%s` = VALUES(`%s`)", c, c)
	}

	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(
		"INSERT INTO `%s` (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s",
		table, strings.Join(cols, ", "), strings.Join(placeholders, ", "),
		strings.Join(assignments, ", "),
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
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
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
