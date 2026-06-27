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
			ID:          "sqlite_query",
			Version:     "1.0",
			Label:       "SQLite",
			Subtitle:    "Query",
			Color:       "#0a6abf",
			Icon:        "database",
			BrandLogo:   "/brands/sqlite.svg",
			Category:    "io",
			Provider:    "internal",
			Integration: "SQLite",
			Tags:        []string{"sqlite", "sql", "database", "query", "select"},
			Description: "Run a SELECT against a SQLite file in your workspace and get rows back. Use ? placeholders in the SQL and pass values through the params array so user-supplied data is safely escaped.",
			Summary:     "Run a parameterized SELECT against a workspace-sandboxed SQLite file and emit rows plus column names.",
			Examples: []core.ParamsExample{
				{
					Title:  "Latest signups",
					Params: json.RawMessage(`{"path":"data/app.db","sql":"SELECT id, email, created_at FROM signups ORDER BY id DESC LIMIT 50"}`),
				},
				{
					Title:  "Filter with a placeholder",
					Params: json.RawMessage(`{"path":"data/app.db","sql":"SELECT * FROM orders WHERE status = ? AND total > ?","params":["paid",100],"limit":500}`),
					Notes:  "Values for ? placeholders go in params, in order.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":   {"type":"string","format":"workspace-path","title":"Database file"},
					"sql":    {"type":"string","title":"SQL"},
					"params": {"type":"array","items":{},"title":"Query values"},
					"limit":  {"type":"integer","minimum":1,"title":"Row limit"}
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
	// Validate sql/params/limit before the sandbox probe so a bad query
	// fails with bad_param regardless of whether the file exists.
	qp, errRes := parseQueryParams(job)
	if errRes != nil {
		return *errRes, nil
	}
	if job.WorkspaceRoot == "" {
		return params.Err(job, "no_sandbox", "sqlite_query requires a workspace sandbox"), nil
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

	return runQueryParsed(ctx, job, sqlConn{db: db}, qp)
}
