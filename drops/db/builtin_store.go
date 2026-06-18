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

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/drops/internal/limits"
	"git.sr.ht/~klahr/hazyflow/drops/internal/params"
	"git.sr.ht/~klahr/hazyflow/engine"
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
			ID:          "builtin_store_append",
			Version:     "1.0",
			Label:       "Built-in store",
			Subtitle:    "Save",
			Color:       "#0a6abf",
			Icon:        "database",
			Category:    "io",
			Provider:    "internal",
			Integration: "Built-in store",
			// "results"/"dashboard"/"report" tag this as the writer behind
			// the in-app Results page (web /results). Tags (not SearchBoost)
			// because a blanket boost would also lift it for "save"/"database",
			// disturbing the deliberate ranking below SQLite Insert rows for
			// those generic verbs (see SearchBoost note there).
			Tags:        []string{"store", "database", "save", "append", "no-setup", "results", "dashboard", "report"},
			Description: "Save rows to a built-in table — no database to set up and no connection string to paste. Pick a table name and the rows land there; the table is created automatically the first time. Each workspace has its own private store, and the saved rows show up on the Results page so you can browse them in-app.",
			Summary:     "Append rows to a workspace-local table with zero setup; auto-creates the table, evolves columns on the fly, and surfaces the rows on the Results page.",
			Examples: []core.ParamsExample{
				{
					Title:  "Capture form submissions",
					Params: json.RawMessage(`{"table":"leads"}`),
					Notes:  "Wire a form/webhook body straight into the rows port — a single object is wrapped into a one-row list automatically.",
				},
				{
					Title:  "Save with a typed column",
					Params: json.RawMessage(`{"table":"signups","column_types":{"age":"INTEGER"}}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "rows", Label: "Rows", Required: true, MIME: []string{"application/json"}},
				{Port: "headers", Label: "Headers", Required: false, MIME: []string{"application/json"}},
			},
			Outputs: []core.Port{
				{Port: "inserted", Label: "Rows saved", MIME: []string{"application/json"}},
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
			ID:          "builtin_store_query",
			Version:     "1.0",
			Label:       "Built-in store",
			Subtitle:    "Read",
			Color:       "#0a6abf",
			Icon:        "database",
			Category:    "io",
			Provider:    "internal",
			Integration: "Built-in store",
			Tags:        []string{"store", "database", "read", "query", "select", "no-setup"},
			Description: "Read rows back out of the built-in store with a SELECT — handy for building a report from data you saved earlier. Use ? placeholders and the params list for any user-supplied values.",
			Summary:     "Run a SELECT against the workspace's built-in store and emit rows plus column names; empty store returns an empty result.",
			Examples: []core.ParamsExample{
				{
					Title:  "Latest 50 leads",
					Params: json.RawMessage(`{"sql":"SELECT * FROM leads ORDER BY submitted_at DESC LIMIT 50"}`),
				},
				{
					Title:  "Filter by status with a placeholder",
					Params: json.RawMessage(`{"sql":"SELECT email, submitted_at FROM leads WHERE status = ?","params":["new"],"limit":200}`),
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
					"sql":    {"type":"string","title":"SQL","description":"A SELECT to run against the built-in store.","examples":["SELECT * FROM leads ORDER BY submitted_at DESC LIMIT 50"]},
					"params": {"type":"array","items":{},"title":"Query values","description":"Values for any ? placeholders in the SQL, in order."},
					"limit":  {"type":"integer","minimum":1,"title":"Row limit","description":"Optional cap on the number of rows returned."}
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
		colTypes, err := parseColumnTypes(job.Params)
		if err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		if err := ensureTable(db, table, headers, colTypes); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		// Schema evolution: when the table already exists, ensureTable
		// is a CREATE-IF-NOT-EXISTS no-op and any headers added since
		// would silently break the upcoming INSERT. The built-in store
		// is explicitly the no-schema-management path — Maria edits her
		// form, adds "phone", and expects new submissions to land. Add
		// any missing columns now (sqlite_insert_rows keeps its
		// stricter behaviour; that drop is for users who manage their
		// own schema).
		if err := evolveBuiltinStoreColumns(db, table, headers, colTypes); err != nil {
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

// evolveBuiltinStoreColumns adds any header not already present on
// table as a new column with the same type-defaulting rules
// ensureTable uses (TEXT unless overridden by colTypes). Built-in
// store only — the regular sqlite/postgres drops leave schema
// management to the user. Idempotent: re-running with no new headers
// is a single PRAGMA read and zero writes.
//
// SQLite caveats:
//   - PRAGMA table_info returns column names case-sensitively as stored
//     in the schema. ADD COLUMN with a differently-cased duplicate would
//     create a conflicting column; we keep the comparison case-sensitive
//     to mirror SQLite's own behaviour.
//   - ALTER TABLE ADD COLUMN cannot add a NOT NULL column without a
//     DEFAULT in SQLite. The store never asks for NOT NULL columns
//     (TEXT default, no constraints), so this restriction doesn't bite.
func evolveBuiltinStoreColumns(db *sql.DB, table string, headers []string, colTypes map[string]string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdent(table)))
	if err != nil {
		return fmt.Errorf("read columns: %w", err)
	}
	defer rows.Close()
	existing := make(map[string]struct{})
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan column: %w", err)
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate columns: %w", err)
	}
	for _, h := range headers {
		if _, ok := existing[h]; ok {
			continue
		}
		t := "TEXT"
		if v, ok := colTypes[h]; ok && v != "" {
			t = v
		}
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s",
			quoteIdent(table), quoteIdent(h), t)
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("add column %q: %w", h, err)
		}
	}
	return nil
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
		// limit=0 means "no user-imposed cap" — but the whole result set is
		// buffered in memory, so an unbounded SELECT would OOM the daemon.
		// Fail fast at the shared row ceiling rather than letting it grow.
		if len(out) > limits.MaxRows() {
			return params.Err(job, "too_many_rows",
				fmt.Sprintf("query returned more than the %d-row limit; add a LIMIT clause, set the 'limit' param, or raise HAZYFLOW_MAX_ROWS", limits.MaxRows())), nil
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
