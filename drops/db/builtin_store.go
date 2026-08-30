// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

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
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
	_ "modernc.org/sqlite"
)

// builtinStorePath is the fixed, workspace-local SQLite file the
// Collections store drops read and write. It lives under a dotted dir so
// it doesn't clutter the user's visible workspace files. The whole
// point of these drops is that a non-technical user gets a place to
// keep rows WITHOUT provisioning Postgres or even picking a filename —
// "save it somewhere" just works. Power users who outgrow it graduate
// to sqlite_* (pick your own file) or postgres_* (bring a DSN).
const builtinStorePath = ".dazyflow-store/data.db"

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "builtin_store_append",
			Version:     "1.0",
			Label:       "Collections",
			Subtitle:    "Save rows",
			Color:       "#0a6abf",
			Icon:        "database",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			// "results"/"dashboard"/"report" tag this as the writer behind
			// the in-app Collections page (web /results). Tags (not SearchBoost)
			// because a blanket boost would also lift it for "save"/"database",
			// disturbing the deliberate ranking below SQLite Insert rows for
			// those generic verbs (see SearchBoost note there).
			Tags:        []string{"collection", "collections", "store", "database", "save", "append", "no-setup", "results", "dashboard", "report"},
			Description: "Save rows to a collection — no database to set up and no connection string to paste. Pick a collection name and the rows land there; the collection is created automatically the first time. Each workspace has its own private Collections, and the saved rows show up under Collections so you can browse them in-app. Every row is stamped with the time it was saved (a saved_at column) so you can sort newest-first. By default every run appends; set “Unique by” to a key column (like date) and a row with a matching key is updated in place instead of piling up a duplicate — so re-running the flow stays idempotent.",
			Summary:     "Append rows to a workspace-local collection with zero setup; auto-creates the collection, evolves columns on the fly, and surfaces the rows under Collections.",
			Examples: []core.ParamsExample{
				{
					Title:  "Capture form submissions",
					Params: json.RawMessage(`{"table":"leads"}`),
					Notes:  "Connect a form/webhook body straight into the rows port — a single object is wrapped into a one-row list automatically.",
				},
				{
					Title:  "Save with a typed column",
					Params: json.RawMessage(`{"table":"signups","column_types":{"age":"INTEGER"}}`),
				},
				{
					Title:  "Idempotent save (no duplicates)",
					Params: json.RawMessage(`{"table":"forecast","unique_by":["date"]}`),
					Notes:  "Re-running updates each date's row in place instead of appending duplicates.",
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
					"table":        {"type":"string","format":"collection-name","title":"Collection","description":"Name of the collection to save into, e.g. leads or signups. Pick one you already have, or type a new name and it's created the first time rows arrive.","examples":["leads"]},
					"unique_by":    {"type":"array","items":{"type":"string"},"format":"collection-columns","title":"Unique by","description":"Optional. Column(s) that identify a row, e.g. date. When set, re-saving a row with the same key updates it in place instead of adding a duplicate, so re-running the flow is idempotent. Leave empty to always append."},
					"column_types": {"type":"object","additionalProperties":{"type":"string"},"description":"Optional: force a column's type (e.g. {\"age\":\"INTEGER\"}). Everything defaults to text, which is fine for most things."},
					"timestamp_column": {"type":"string","title":"Time column","description":"Every saved row is stamped with the time it was saved, in a column called saved_at, so you can sort newest-first. Rename it here, or set it to empty to turn the stamp off. If your own rows already include a column of that name, yours is kept.","default":"saved_at"}
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
			Label:       "Collections",
			Subtitle:    "Query with SQL",
			Color:       "#0a6abf",
			Icon:        "database",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			Tags:        []string{"collection", "collections", "store", "database", "read", "query", "select", "no-setup"},
			Description: "Read rows back out of a collection with a SELECT — handy for building a report from data you saved earlier. Use ? placeholders and the params list for any user-supplied values.",
			Summary:     "Run a SELECT against the workspace's Collections and emit rows plus column names; an empty collection returns an empty result.",
			Examples: []core.ParamsExample{
				{
					Title:  "Latest 50 leads",
					Params: json.RawMessage(`{"sql":"SELECT * FROM leads ORDER BY saved_at DESC LIMIT 50"}`),
				},
				{
					Title:  "Filter by status with a placeholder",
					Params: json.RawMessage(`{"sql":"SELECT email, saved_at FROM leads WHERE status = ?","params":["new"],"limit":200}`),
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
					"sql":    {"type":"string","title":"SQL","description":"A SELECT to run against your Collections.","examples":["SELECT * FROM leads ORDER BY saved_at DESC LIMIT 50"]},
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
		r := params.Err(job, "no_sandbox", "Collections requires a workspace sandbox")
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
func executeBuiltinStoreAppend(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
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
	// frictionless path is the whole point of the Collections store.
	inline := rowsRef.Inline
	if m, isObj := inline.(map[string]any); isObj {
		inline = []any{m}
	}
	rows, err := normalizeRows(inline)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	var headers []string
	// Prefer the column order folded onto the rows value itself; fall back to
	// deriving from the row keys.
	if len(rowsRef.Headers) > 0 {
		headers = rowsRef.Headers
	}
	if headers == nil {
		headers = deriveHeaders(rows)
	}
	for _, h := range headers {
		if err := validateIdent(h); err != nil {
			return params.Err(job, "bad_input", fmt.Sprintf("column %q: %v", h, err)), nil
		}
	}

	// Stamp when each row was saved. Without this a collection answers "what
	// did people say" but never "when did this arrive" — and the Find/Query
	// steps offer a "Sort by" that had no time column to point at, so the
	// obvious "newest first" was impossible on the very path (form → save)
	// this store exists for. The column is added to the END of the header
	// list so it never disturbs the owner's own column order.
	//
	// Skipped when the incoming rows already carry a column of that name: the
	// caller's own value wins over ours (a form that posts its own
	// submitted_at, a re-import that carries original timestamps).
	//
	// Only when there is something to save: stamping an empty payload would
	// add a saved_at column (and so CREATE the collection) for a run that
	// saved no rows, turning a no-op into a schema change.
	if tsCol, tsErr := timestampColumn(job, headers); tsErr != nil {
		return params.Err(job, "bad_param", tsErr.Error()), nil
	} else if tsCol != "" && len(rows) > 0 {
		stamp := time.Now().UTC().Format(time.RFC3339)
		for _, row := range rows {
			row[tsCol] = stamp
		}
		headers = append(headers, tsCol)
	}

	uniqueBy, keyErr := parseUniqueBy(job, headers)
	if keyErr != nil {
		return *keyErr, nil
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
		if err := sqliteEnsureTable(db, table, headers, colTypes); err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		// Schema evolution: when the table already exists, ensureTable
		// is a CREATE-IF-NOT-EXISTS no-op and any headers added since
		// would silently break the upcoming INSERT. The Collections store
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
	// With a "Unique by" key, upsert on those columns so re-saving a row with
	// the same key updates it in place (idempotent re-runs) rather than adding a
	// duplicate. Without it, the historical append behaviour is unchanged.
	if len(uniqueBy) > 0 {
		if err := ensureUniqueIndex(db, table, uniqueBy); err != nil {
			return params.Err(job, "not_unique", err.Error()), nil
		}
		stmt := insertSQL(sqliteDialect{}, quoteIdent(table), headers, sqliteDialect{}.upsertClause(uniqueBy, subtract(headers, uniqueBy)))
		saved, err := sqlConn{db: db}.execBatch(ctx, stmt, headers, rows, "upsert")
		if err != nil {
			return params.Err(job, "db", err.Error()), nil
		}
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"inserted": {MIME: "application/json", Inline: saved}},
		}, nil
	}

	inserted, err := sqlConn{db: db}.execBatch(ctx, insertSQL(sqliteDialect{}, quoteIdent(table), headers, ""), headers, rows, "insert")
	if err != nil {
		return params.Err(job, "db", err.Error()), nil
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"inserted": {MIME: "application/json", Inline: inserted}},
	}, nil
}

// defaultTimestampColumn is the column the Collections store stamps each
// saved row with. Named for what it records — when the row was SAVED — rather
// than when anything happened upstream, which only the caller can know.
const defaultTimestampColumn = "saved_at"

// timestampColumn resolves which column (if any) should carry the save time.
// Defaults to saved_at; the "timestamp_column" param renames it (to match an
// existing convention like submitted_at) or, set to "", turns it off for
// owners who want the collection to hold exactly the columns they send.
//
// Returns "" when the incoming rows already declare that column, so a value
// the caller supplied is never overwritten by ours.
func timestampColumn(job core.Job, headers []string) (string, error) {
	col := defaultTimestampColumn
	if raw, ok := job.Params["timestamp_column"]; ok && raw != nil {
		s, isStr := raw.(string)
		if !isStr {
			return "", fmt.Errorf("timestamp_column must be a column name, or \"\" to turn the timestamp off")
		}
		col = strings.TrimSpace(s)
		if col == "" {
			return "", nil
		}
	}
	if err := validateIdent(col); err != nil {
		return "", fmt.Errorf("timestamp column %q: %w", col, err)
	}
	for _, h := range headers {
		if strings.EqualFold(h, col) {
			return "", nil // the caller supplies it; don't clobber
		}
	}
	return col, nil
}

// parseUniqueBy reads the optional "unique_by" key columns for an idempotent
// upsert. Empty → plain append. Each key must be a valid identifier and (when
// there are rows to write) one of the saved columns — there'd be no value to
// match on otherwise.
func parseUniqueBy(job core.Job, headers []string) ([]string, *core.Result) {
	if raw, ok := job.Params["unique_by"]; !ok || raw == nil {
		return nil, nil // optional — absent means plain append
	}
	keys, err := paramStringArray(job.Params, "unique_by")
	if err != nil {
		r := params.Err(job, "bad_param", err.Error())
		return nil, &r
	}
	if len(keys) == 0 {
		return nil, nil
	}
	headerSet := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		headerSet[h] = struct{}{}
	}
	for _, k := range keys {
		if err := validateIdent(k); err != nil {
			r := params.Err(job, "bad_param", fmt.Sprintf("unique-by column %q: %v", k, err))
			return nil, &r
		}
		if len(headers) > 0 {
			if _, ok := headerSet[k]; !ok {
				r := params.Err(job, "bad_param", fmt.Sprintf("unique-by column %q isn't one of the saved columns", k))
				return nil, &r
			}
		}
	}
	return keys, nil
}

// ensureUniqueIndex creates a UNIQUE index on the key columns so the store can
// upsert (ON CONFLICT) on them; IF NOT EXISTS makes it a no-op once present.
// SQLite refuses to build the index when existing rows already collide on the
// key — surfaced as a clear, fixable error rather than a silent no-op.
func ensureUniqueIndex(db *sql.DB, table string, keys []string) error {
	idx := "ux_" + table + "_" + strings.Join(keys, "_")
	stmt := fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)",
		quoteIdent(idx), quoteIdent(table), strings.Join(quoteAll(sqliteDialect{}, keys), ", "))
	if _, err := db.Exec(stmt); err != nil {
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "unique") || strings.Contains(msg, "duplicate") {
			cols := strings.Join(keys, ", ")
			return fmt.Errorf("this collection already has duplicate rows for %s, so it can't be made unique on %s — remove the duplicates or choose a different key", cols, cols)
		}
		return fmt.Errorf("create unique index: %w", err)
	}
	return nil
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
				fmt.Sprintf("query returned more than the %d-row limit; add a LIMIT clause, set the 'limit' param, or raise DAZYFLOW_MAX_ROWS", limits.MaxRows())), nil
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
