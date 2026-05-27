package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	"git.sr.ht/~klahr/hazy-flow/integrations/internal/params"
	_ "modernc.org/sqlite"
)

// builtinStorePath is the fixed, workspace-local SQLite file the
// built-in store drops read and write. It lives under a dotted dir so
// it doesn't clutter the user's visible workspace files. The whole
// point of these drops is that a non-technical user gets a place to
// keep rows WITHOUT provisioning Postgres or even picking a filename —
// "save it somewhere" just works. Power users who outgrow it graduate
// to sqlite_* (pick your own file) or postgres_* (bring a DSN).
const builtinStorePath = ".hazyflow-store/data.db"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "builtin_store_append",
			Version:        "1.0",
			Label:          "Save to built-in store",
			Color:          "#0a6abf",
			Icon:           "database",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Built-in store",
			Tags:           []string{"store", "database", "save", "append", "no-setup"},
			Description:    "Save rows to a built-in table — no database to set up and no connection string to paste. Pick a table name and the rows land there; the table is created automatically the first time. Each workspace has its own private store.",
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
					"table":        {"type":"string","description":"Name of the table to save into, e.g. leads or signups. Created automatically the first time.","examples":["leads"]},
					"column_types": {"type":"object","additionalProperties":{"type":"string"},"description":"Optional: force a column's type (e.g. {\"age\":\"INTEGER\"}). Everything defaults to text, which is fine for most things."}
				},
				"required":["table"]
			}`),
		},
		Execute: executeBuiltinStoreAppend,
	})

	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:             "builtin_store_query",
			Version:        "1.0",
			Label:          "Read from built-in store",
			Color:          "#0a6abf",
			Icon:           "database",
			Category:       "io",
			Provider:       "internal",
			Integration:    "Built-in store",
			Tags:           []string{"store", "database", "read", "query", "select", "no-setup"},
			Description:    "Read rows back out of the built-in store with a SELECT — handy for building a report from data you saved earlier. Use ? placeholders and the params list for any user-supplied values.",
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"sql":    {"type":"string","description":"A SELECT to run against the built-in store.","examples":["SELECT * FROM leads ORDER BY submitted_at DESC LIMIT 50"]},
					"params": {"type":"array","items":{},"description":"Values for any ? placeholders in the SQL, in order."},
					"limit":  {"type":"integer","minimum":1,"description":"Optional cap on the number of rows returned."}
				},
				"required":["sql"]
			}`),
			Idempotent: true,
		},
		Execute: executeBuiltinStoreQuery,
	})
}

// openBuiltinStore resolves the fixed store path through the workspace
// os.Root (creating the parent dir on first write) and opens it via
// database/sql. Mirrors the sandbox discipline of sqlite_insert_rows:
// os.Root validates the path before sqlite — which takes an
// unconstrained filename — ever sees it. The path is a constant, so
// escape is impossible here; the os.Root dance is kept for symmetry and
// to create the parent directory safely.
func openBuiltinStore(job core.Job, create bool) (*sql.DB, *core.Result) {
	if job.WorkspaceRoot == "" {
		r := params.Err(job, "no_sandbox", "the built-in store requires a workspace sandbox")
		return nil, &r
	}
	root, err := os.OpenRoot(job.WorkspaceRoot)
	if err != nil {
		r := params.Err(job, "sandbox", fmt.Sprintf("open root: %v", err))
		return nil, &r
	}
	if create {
		if dir := filepath.Dir(builtinStorePath); dir != "" && dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				root.Close()
				r := params.Err(job, "io", fmt.Sprintf("mkdir store dir: %v", err))
				return nil, &r
			}
		}
		probe, probeErr := root.OpenFile(builtinStorePath, os.O_RDWR|os.O_CREATE, 0o644)
		root.Close()
		if probeErr != nil {
			r := params.Err(job, "io", fmt.Sprintf("open store: %v", probeErr))
			return nil, &r
		}
		probe.Close()
	} else {
		// Read path: a store that's never been written to simply has no
		// file yet — that's an empty store, not an error. Signal that to
		// the caller with a nil db and nil result.
		probe, probeErr := root.Open(builtinStorePath)
		root.Close()
		if probeErr != nil {
			if errors.Is(probeErr, fs.ErrNotExist) {
				return nil, nil
			}
			r := params.Err(job, "io", fmt.Sprintf("open store: %v", probeErr))
			return nil, &r
		}
		probe.Close()
	}

	absPath := filepath.Join(job.WorkspaceRoot, builtinStorePath)
	db, err := sql.Open("sqlite", absPath)
	if err != nil {
		r := params.Err(job, "db", fmt.Sprintf("open store: %v", err))
		return nil, &r
	}
	return db, nil
}

// executeBuiltinStoreAppend is the no-DSN twin of sqlite_insert_rows:
// same batch-insert-in-one-transaction behaviour, but the database file
// is fixed and auto-created so the user never sees a path or connection
// string. The table is always auto-created from the row shape.
func executeBuiltinStoreAppend(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	table, err := params.String(job.Params, "table")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if err := validateIdent(table); err != nil {
		return params.Err(job, "bad_param", fmt.Sprintf("table name %q: %v", table, err)), nil
	}
	rowsRef, ok := job.Input["rows"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'rows' is required"), nil
	}
	// A webhook/form body is a single {field: value} object, but the
	// store appends row *lists*. Wrap a lone object into a one-row list
	// so "form → save" works without a reshape step in between — that
	// frictionless path is the whole point of the built-in store.
	inline := rowsRef.Inline
	if m, isObj := inline.(map[string]any); isObj {
		inline = []any{m}
	}
	rows, err := normalizeRows(inline)
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

	db, errResult := openBuiltinStore(job, true)
	if errResult != nil {
		return *errResult, nil
	}
	defer db.Close()

	if len(headers) > 0 {
		colTypes, _ := paramStringMap(job.Params, "column_types")
		if err := ensureTable(db, table, headers, colTypes); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
	}
	if len(rows) == 0 {
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"inserted": {MIME: "application/json", Inline: 0}},
		}, nil
	}
	inserted, err := insertBatch(db, table, headers, rows)
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"inserted": {MIME: "application/json", Inline: inserted}},
	}, nil
}

// executeBuiltinStoreQuery is the no-DSN twin of sqlite_query. A store
// that's never been written to returns no rows rather than erroring —
// an empty store is a valid state, not a misconfiguration.
func executeBuiltinStoreQuery(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	sqlText, err := params.String(job.Params, "sql")
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if sqlText == "" {
		return params.Err(job, "bad_param", "sql is empty"), nil
	}

	var args []any
	if v, ok := job.Params["params"]; ok && v != nil {
		raw, ok := v.([]any)
		if !ok {
			return params.Err(job, "bad_param", fmt.Sprintf("params: expected array, got %T", v)), nil
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

	db, errResult := openBuiltinStore(job, false)
	if errResult != nil {
		return *errResult, nil
	}
	if db == nil {
		// Store has never been written to — return an empty result.
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{
				"rows":    {MIME: "application/json", Inline: []map[string]any{}},
				"columns": {MIME: "application/json", Inline: []string{}},
			},
		}, nil
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
