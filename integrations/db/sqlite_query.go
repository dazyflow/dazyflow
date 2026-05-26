package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
	_ "modernc.org/sqlite"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "sqlite_query",
			Version:        "1.0",
			Label:          "SQLite query",
			Color:          "#0a6abf",
			Icon:           "database",
			BrandLogo:      "/brands/sqlite.svg",
			Category:       "io",
			Provider:       "internal",
			Integration:    "SQLite",
			Tags:           []string{"sqlite", "sql", "database", "query", "select"},
			Description:    "Run a SELECT against a SQLite file in your workspace and get rows back. Use ? placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":   {"type":"string","format":"workspace-path"},
					"sql":    {"type":"string"},
					"params": {"type":"array","items":{}},
					"limit":  {"type":"integer","minimum":1}
				},
				"required":["path","sql"]
			}`),
			Idempotent: true,
		},
		Execute: executeSQLiteQuery,
	})
}

// executeSQLiteQuery runs a single SELECT against a SQLite database
// file inside the workspace sandbox and emits the rows as the same
// {column: value}[] shape postgres_query produces — wire-up
// compatible with excel_write, postgres_insert_rows, the lot.
//
// SQLite's dynamic typing means cell values come back as the Go type
// the storage class decoded to: int64 for INTEGER, float64 for REAL,
// string for TEXT, []byte for BLOB. JSON serialization handles all of
// these (binary becomes base64). Unlike Postgres, there's no NUMERIC
// pseudo-type wrapper to unwrap.
//
// Sandbox: same os.Root probe as sqlite_insert_rows — the user-
// supplied path is workspace-relative and validated through the
// sandbox root before sqlite (which accepts unconstrained filenames)
// is allowed to see it.
func executeSQLiteQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	path, err := params.String(job.Params, "path")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	sqlText, err := params.String(job.Params, "sql")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if sqlText == "" {
		return params.Err(job, "bad_param", "sql is empty"), nil
	}
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "sqlite_query requires a workspace sandbox"), nil
	}

	var args []any
	if v, ok := job.Params["params"]; ok && v != nil {
		raw, ok := v.([]any)
		if !ok {
			return params.Err(job, "bad_param",
				fmt.Sprintf("params: expected array, got %T", v)), nil
		}
		args = raw
	}

	limit := 0
	if n, ok := paramInt(job.Params, "limit"); ok {
		if n < 0 {
			return params.Err(job, "bad_param", "limit must be >= 0"), nil
		}
		limit = n
	}

	// Probe-open the path through os.Root so a hostile params payload
	// can't read a sqlite database outside the workspace.
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		return params.Err(job, "sandbox", fmt.Sprintf("open root: %v", err)), nil
	}
	probe, probeErr := root.Open(path)
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

	rows, err := db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("query: %v", err)), nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("columns: %v", err)), nil
	}

	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		// database/sql scans by reference, so we need a slice of
		// pointers-into-vals to receive the row, then read vals back
		// into a map.
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return params.Err(job, "db", fmt.Sprintf("scan row %d: %v", len(out), err)), nil
		}
		rec := make(map[string]any, len(columns))
		for i, c := range columns {
			rec[c] = vals[i]
		}
		out = append(out, rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return params.Err(job, "db", fmt.Sprintf("iterate: %v", err)), nil
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":    {MIME: "application/json", Inline: out},
			"columns": {MIME: "application/json", Inline: columns},
		},
	}, nil
}
