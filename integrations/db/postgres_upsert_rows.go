package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"github.com/jackc/pgx/v5/pgxpool"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "postgres_upsert_rows",
			Version:        "1.0",
			Label:          "Postgres upsert rows",
			Color:          "#336791",
			Icon:           "database",
			BrandLogo:      "/brands/postgres.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Postgres",
			Tags:           []string{"postgres", "postgresql", "sql", "database", "upsert", "merge", "etl"},
			Description:    "INSERT ... ON CONFLICT (...) DO UPDATE against Postgres. 'conflict_columns' lists the unique/PK columns Postgres matches on; 'update_columns' (optional) restricts which columns get updated — default is all non-conflict columns. When create_table=true the table is created with a UNIQUE constraint on the conflict columns.",
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
					"schema":           {"type":"string","default":"public"},
					"table":            {"type":"string"},
					"conflict_columns": {"type":"array","items":{"type":"string"}},
					"update_columns":   {"type":"array","items":{"type":"string"}},
					"create_table":     {"type":"boolean","default":true,"description":"Auto-create the table (with a UNIQUE on conflict_columns) when missing. Defaults true."},
					"column_types":     {"type":"object","additionalProperties":{"type":"string"}}
				},
				"required":["dsn","table","conflict_columns"]
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
	dsn, err := paramString(job.Params, "dsn")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	table, err := paramString(job.Params, "table")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if err := validateIdent(table); err != nil {
		return errResult(job, "bad_param", fmt.Sprintf("table name %q: %v", table, err)), nil
	}
	schema := "public"
	if s, ok := paramStringOpt(job.Params, "schema"); ok && s != "" {
		schema = s
	}
	if err := validateIdent(schema); err != nil {
		return errResult(job, "bad_param", fmt.Sprintf("schema name %q: %v", schema, err)), nil
	}

	conflictCols, err := paramStringArray(job.Params, "conflict_columns")
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	if len(conflictCols) == 0 {
		return errResult(job, "bad_param", "conflict_columns must list at least one column"), nil
	}
	for _, c := range conflictCols {
		if err := validateIdent(c); err != nil {
			return errResult(job, "bad_param", fmt.Sprintf("conflict column %q: %v", c, err)), nil
		}
	}

	// update_columns: nil = "use all non-conflict columns" (the default
	// upsert semantics); empty slice present in params = "DO NOTHING"
	// (explicit user intent to skip updates). We distinguish via the
	// param's presence, not just its length.
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
			if err := validateIdent(c); err != nil {
				return errResult(job, "bad_param", fmt.Sprintf("update column %q: %v", c, err)), nil
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
		if err := validateIdent(h); err != nil {
			return errResult(job, "bad_input", fmt.Sprintf("column %q: %v", h, err)), nil
		}
	}
	// conflict_columns must be present in headers — otherwise we have
	// no value to plug into the conflict target.
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

	pool, err := defaultPGRegistry.pgPool(ctx, job.Tenant, dsn)
	if err != nil {
		return errResult(job, "db", fmt.Sprintf("connect: %v", err)), nil
	}

	qualified := fmt.Sprintf("%s.%s", quoteIdent(schema), quoteIdent(table))

	createTable := true
	if v, present := paramBool(job.Params, "create_table"); present {
		createTable = v
	}
	if createTable && len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := pgEnsureTableWithUnique(ctx, pool, qualified, headers, colTypes, conflictCols); err != nil {
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

	// Decide the final update set:
	//   - explicit []        → DO NOTHING
	//   - explicit non-empty → use those (validated above)
	//   - absent             → derive (headers minus conflict_columns)
	if !updateColsExplicit {
		updateCols = subtract(headers, conflictCols)
	}

	processed, err := pgUpsertBatch(ctx, pool, qualified, headers, conflictCols, updateCols, rows)
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

// pgEnsureTableWithUnique extends pgEnsureTable with a UNIQUE
// constraint on the conflict columns — required for ON CONFLICT to
// have a target. For existing tables the user is on the hook to make
// sure the constraint exists; we don't try to ALTER it.
func pgEnsureTableWithUnique(ctx context.Context, pool *pgxpool.Pool, qualified string, headers []string, colTypes map[string]string, conflictCols []string) error {
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
		qualified, strings.Join(cols, ", "), strings.Join(uniqueCols, ", "))
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	return nil
}

// pgUpsertBatch runs all rows in one transaction. The generated
// statement looks like:
//
//	INSERT INTO "schema"."table" ("a","b","c")
//	VALUES ($1,$2,$3)
//	ON CONFLICT ("a") DO UPDATE
//	  SET "b" = EXCLUDED."b", "c" = EXCLUDED."c"
//
// EXCLUDED is Postgres's pseudo-table referencing the row we tried to
// insert; without it we'd be re-binding the same parameters.
//
// When updateCols is empty we substitute DO NOTHING — explicit
// "insert if absent, leave existing alone" semantics that's a common
// idempotency pattern for event ingestion.
func pgUpsertBatch(ctx context.Context, pool *pgxpool.Pool, qualified string, headers, conflictCols, updateCols []string, rows []map[string]any) (int, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	cols := make([]string, len(headers))
	placeholders := make([]string, len(headers))
	for i, h := range headers {
		cols[i] = quoteIdent(h)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
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
			assignments[i] = fmt.Sprintf("%s = EXCLUDED.%s", q, q)
		}
		conflictClause = fmt.Sprintf("ON CONFLICT (%s) DO UPDATE SET %s",
			strings.Join(conflictList, ", "), strings.Join(assignments, ", "))
	}

	stmt := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s) %s",
		qualified, strings.Join(cols, ", "), strings.Join(placeholders, ", "), conflictClause,
	)

	count := 0
	for i, row := range rows {
		args := make([]any, len(headers))
		for j, h := range headers {
			args[j] = row[h]
		}
		if _, err := tx.Exec(ctx, stmt, args...); err != nil {
			return 0, fmt.Errorf("upsert row %d: %w", i, err)
		}
		count++
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return count, nil
}

// subtract returns the elements of a that aren't in b, preserving
// order. Used to default update_columns to (headers \ conflict_columns).
func subtract(a, b []string) []string {
	skip := make(map[string]struct{}, len(b))
	for _, x := range b {
		skip[x] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if _, ok := skip[x]; ok {
			continue
		}
		out = append(out, x)
	}
	return out
}
