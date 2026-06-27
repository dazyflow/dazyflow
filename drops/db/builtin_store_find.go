// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/drops/internal/rowcel"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "builtin_store_find",
			Version:     "1.0",
			Label:       "Collections",
			Subtitle:    "Find rows",
			Color:       "#0a6abf",
			Icon:        "table",
			Category:    "io",
			Provider:    "internal",
			Integration: "Collections",
			Tags:        []string{"collection", "collections", "store", "database", "read", "find", "filter", "search", "no-setup", "no-code", "results"},
			Description: "Read rows back out of a collection without writing any SQL. Pick the collection, then add simple conditions — like status equals unpaid, or amount greater than 100 — with the visual editor, and the matching rows come out. Optionally sort by a column and cap how many rows you get. The friendly companion to “Save rows”; reach for “Query rows” when you want raw SQL.",
			Summary:     "Read rows from one collection with no-code filters (column / operator / value), an optional sort and limit; emits rows plus column names.",
			Examples: []core.ParamsExample{
				{
					Title:  "Unpaid invoices",
					Params: json.RawMessage(`{"table":"invoices","filter":"row.status == \"unpaid\""}`),
					Notes:  "Build the condition with the visual editor — you don't type the expression by hand.",
				},
				{
					Title:  "Newest 20 leads",
					Params: json.RawMessage(`{"table":"leads","sort_by":"submitted_at","sort_dir":"desc","limit":20}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "table", Label: "Collection", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "columns", Label: "Columns", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"table":    {"type":"string","format":"collection","title":"Collection","description":"Name of the collection to read from, e.g. leads or invoices. Overridden by a value wired into the Collection input."},
					"filter":   {"type":"string","format":"row-condition","x_columns_source":"collection","title":"Where","description":"Keep only rows that match these conditions. Leave empty to return every row."},
					"sort_by":  {"type":"string","format":"collection-column","title":"Sort by","description":"Column to sort by. Leave blank to keep the order rows were saved in."},
					"sort_dir": {"type":"string","enum":["asc","desc"],"enumNames":["Ascending (A→Z, low→high)","Descending (Z→A, high→low)"],"default":"asc","title":"Direction","description":"Sort direction. Only applies when a sort column is set."},
					"limit":    {"type":"integer","minimum":1,"title":"Max rows","description":"Optional cap on how many rows to return (applied after filtering)."}
				},
				"required":["table"]
			}`),
			Idempotent: true,
		},
		Execute: executeBuiltinStoreFind,
	})
}

// executeBuiltinStoreFind is the no-SQL reader: pick one collection, apply the
// no-code row-condition filter (the same column/operator/value builder Split
// rows and Route rows use, compiled to CEL via drops/internal/rowcel), then
// sort and cap in memory. It reads the whole collection and filters in
// process — fine for the Collections store's modest sizes, and bounded by the
// shared row ceiling so a runaway collection can't OOM the daemon.
func executeBuiltinStoreFind(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	table, err := resolveTable(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	limit := 0
	if n, ok := paramInt(job.Params, "limit"); ok {
		if n < 0 {
			return params.Err(job, "bad_param", "limit must be >= 0"), nil
		}
		limit = n
	}

	// Compile the optional no-code filter (CEL emitted by the row-condition
	// builder). An empty filter means "return every row".
	var prog cel.Program
	if expr := strings.TrimSpace(params.StringDefault(job.Params, "filter", "")); expr != "" {
		env, err := rowcel.Env()
		if err != nil {
			return params.Err(job, "internal", fmt.Sprintf("cel env: %v", err)), nil
		}
		prog, err = rowcel.Compile(env, expr, "filter")
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
	}

	db, errResult := openBuiltinStore(job, false)
	if errResult != nil {
		return *errResult, nil
	}
	if db == nil {
		// No store exists yet, so the named collection definitely doesn't.
		// Fail loudly rather than returning a silent empty result — a missing
		// collection is almost always a typo, and an empty result hides it.
		return params.Err(job, "no_such_collection",
			fmt.Sprintf("collection %q doesn't exist — no collections have been created yet (save rows to one first)", table)), nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, "SELECT * FROM "+quoteIdent(table))
	if err != nil {
		// A non-existent collection is a hard error — distinct from an existing
		// collection with no matching rows (a valid empty result below). This
		// surfaces a typo'd or stale name instead of silently doing nothing.
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return params.Err(job, "no_such_collection", missingCollectionMsg(ctx, db, table)), nil
		}
		return params.Err(job, "db", fmt.Sprintf("read: %v", err)), nil
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return params.Err(job, "db", fmt.Sprintf("columns: %v", err)), nil
	}

	out := make([]map[string]any, 0, 16)
	scanned := 0
	for rows.Next() {
		vals := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return params.Err(job, "db", fmt.Sprintf("scan row %d: %v", scanned, err)), nil
		}
		rec := make(map[string]any, len(columns))
		for i, c := range columns {
			rec[c] = vals[i]
		}
		scanned++
		// Bound the scan, not just the result: a selective filter over a huge
		// collection would still buffer every row otherwise.
		if scanned > limits.MaxRows() {
			return params.Err(job, "too_many_rows",
				fmt.Sprintf("collection has more than the %d-row scan limit; add conditions to narrow it, or raise DAZYFLOW_MAX_ROWS", limits.MaxRows())), nil
		}
		if prog != nil {
			pass, err := rowcel.EvalBool(prog, rec)
			if err != nil {
				return params.Err(job, "eval", fmt.Sprintf("filter: %v", err)), nil
			}
			if !pass {
				continue
			}
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return params.Err(job, "db", fmt.Sprintf("read: %v", err)), nil
	}

	// Sort in memory: avoids quoting a user column into ORDER BY, and orders
	// TEXT-stored numbers numerically (SQLite would sort them lexically).
	if sortBy := strings.TrimSpace(params.StringDefault(job.Params, "sort_by", "")); sortBy != "" && slices.Contains(columns, sortBy) {
		desc := strings.EqualFold(strings.TrimSpace(params.StringDefault(job.Params, "sort_dir", "asc")), "desc")
		sort.SliceStable(out, func(i, j int) bool {
			less := cellLess(out[i][sortBy], out[j][sortBy])
			if desc {
				return cellLess(out[j][sortBy], out[i][sortBy])
			}
			return less
		})
	}

	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}

	return queryResult(job, out, columns), nil
}

// resolveTable picks the collection name: a value wired into the Collection
// input wins, otherwise the table param (the dropdown). Mirrors the
// coordinate-input pattern of the weather drops, so a name can be computed
// upstream (a form field, a branch, an earlier step) instead of hard-coded.
func resolveTable(job core.Job) (string, error) {
	txt, ok := params.TextInputOr(job, "table", "")
	if !ok {
		return "", errors.New(`'Collection' input must be text (a collection name)`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return s, nil
	}
	if t := strings.TrimSpace(params.StringDefault(job.Params, "table", "")); t != "" {
		return t, nil
	}
	return "", errors.New("pick a collection to read from, or wire a collection name into the Collection input")
}

// missingCollectionMsg builds a "collection X doesn't exist" message, listing
// the collections that DO exist so a typo is easy to spot and fix.
func missingCollectionMsg(ctx context.Context, db *sql.DB, table string) string {
	names := existingCollections(ctx, db)
	if len(names) == 0 {
		return fmt.Sprintf("collection %q doesn't exist — no collections have been created yet (save rows to one first)", table)
	}
	return fmt.Sprintf("collection %q doesn't exist — available collections: %s", table, strings.Join(names, ", "))
}

// existingCollections lists the user tables in the store (best-effort; a query
// error just yields no names, so the caller still reports the missing one).
func existingCollections(ctx context.Context, db *sql.DB) []string {
	rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// cellLess orders two cell values: numerically when both parse as numbers
// (so "9" sorts before "10"), otherwise by their string form. A nil (NULL)
// sorts as an empty string, i.e. before any non-empty value.
func cellLess(a, b any) bool {
	af, aok := cellFloat(a)
	bf, bok := cellFloat(b)
	if aok && bok {
		return af < bf
	}
	return cellString(a) < cellString(b)
}

// cellFloat tries to read a cell as a float64, accepting the numeric types a
// SQLite scan or a numeric TEXT value can carry.
func cellFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// cellString renders a cell for lexical comparison; nil becomes "".
func cellString(v any) string {
	if v == nil {
		return ""
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}
