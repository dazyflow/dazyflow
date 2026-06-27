// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
	_ "modernc.org/sqlite"
)

// TestBuiltinStore_AppendThenQuery exercises the no-DSN store end to
// end: append rows (auto-creating the file + table), then read them
// back via the query drop. No path is ever supplied — that's the point.
func TestBuiltinStore_AppendThenQuery(t *testing.T) {
	root := t.TempDir()

	appendRes, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Alice", "email": "alice@example.com"},
				{"name": "Bob", "email": "bob@example.com"},
			}},
			"headers": {Inline: []string{"name", "email"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("append execute: %v", err)
	}
	if appendRes.Status != core.StatusOK {
		t.Fatalf("append status=%q err=%+v", appendRes.Status, appendRes.Error)
	}
	if got, _ := appendRes.Output["inserted"].Inline.(int); got != 2 {
		t.Fatalf("inserted = %v, want 2", appendRes.Output["inserted"].Inline)
	}

	queryRes, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "SELECT name, email FROM leads ORDER BY name"},
	}, nil)
	if err != nil {
		t.Fatalf("query execute: %v", err)
	}
	if queryRes.Status != core.StatusOK {
		t.Fatalf("query status=%q err=%+v", queryRes.Status, queryRes.Error)
	}
	rows, ok := queryRes.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows wrong type: %T", queryRes.Output["rows"].Inline)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0]["name"] != "Alice" {
		t.Errorf("first row name = %v, want Alice", rows[0]["name"])
	}
}

// TestBuiltinStore_AppendSingleObject verifies the form/webhook path:
// a single {field: value} object (not a list) is accepted and stored as
// one row, so "form → save" needs no reshape step.
func TestBuiltinStore_AppendSingleObject(t *testing.T) {
	root := t.TempDir()
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows": {Inline: map[string]any{"name": "Carol", "email": "carol@example.com"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["inserted"].Inline.(int); got != 1 {
		t.Errorf("inserted = %v, want 1", res.Output["inserted"].Inline)
	}
}

// TestBuiltinStore_AppendEvolvesSchema covers the form-editing path:
// a user appends submissions with one shape, then edits their form to
// add a new field. The store's whole point is that the user doesn't
// manage schema, so the second append should automatically ALTER the
// table to add the new column — not fail with "table has no column
// named X". sqlite_insert_rows still rejects this because that drop is
// for users who manage their own schema; only the built-in path
// evolves.
func TestBuiltinStore_AppendEvolvesSchema(t *testing.T) {
	root := t.TempDir()
	base := core.Job{WorkspaceRoot: root, Params: map[string]any{"table": "leads"}}

	first, err := executeBuiltinStoreAppend(t.Context(), withInput(base, map[string]core.Ref{
		"rows": {Inline: map[string]any{"name": "Alice", "email": "alice@example.com"}},
	}), nil)
	if err != nil {
		t.Fatalf("first append execute: %v", err)
	}
	if first.Status != core.StatusOK {
		t.Fatalf("first append status=%q err=%+v", first.Status, first.Error)
	}

	// Maria edits her form to add a phone field. Second submission
	// includes it — must succeed and persist the value.
	second, err := executeBuiltinStoreAppend(t.Context(), withInput(base, map[string]core.Ref{
		"rows": {Inline: map[string]any{"name": "Bob", "email": "bob@example.com", "phone": "+1 555 0123"}},
	}), nil)
	if err != nil {
		t.Fatalf("second append execute: %v", err)
	}
	if second.Status != core.StatusOK {
		t.Fatalf("schema evolution failed: status=%q err=%+v", second.Status, second.Error)
	}

	// Verify the phone value actually landed (not just that the
	// statement was accepted).
	q, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"sql": "SELECT name, phone FROM leads WHERE name = 'Bob'",
		},
	}, nil)
	if err != nil {
		t.Fatalf("query execute: %v", err)
	}
	rows, _ := q.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after evolution, got %d (%#v)", len(rows), rows)
	}
	if rows[0]["phone"] != "+1 555 0123" {
		t.Errorf("phone = %v, want +1 555 0123", rows[0]["phone"])
	}
	// Existing rows still readable and have NULL phones.
	all, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "SELECT name, phone FROM leads ORDER BY name"},
	}, nil)
	if err != nil {
		t.Fatalf("all query: %v", err)
	}
	allRows, _ := all.Output["rows"].Inline.([]map[string]any)
	if len(allRows) != 2 {
		t.Fatalf("expected 2 rows total, got %d", len(allRows))
	}
	if allRows[0]["phone"] != nil {
		t.Errorf("Alice (pre-evolution row) phone = %v, want NULL", allRows[0]["phone"])
	}
}

func withInput(j core.Job, in map[string]core.Ref) core.Job {
	j.Input = in
	return j
}

// TestBuiltinStore_AppendUniqueByUpsert verifies the opt-in idempotent path:
// with unique_by set, re-saving a row with the same key updates it in place
// instead of appending a duplicate.
func TestBuiltinStore_AppendUniqueByUpsert(t *testing.T) {
	root := t.TempDir()
	base := core.Job{WorkspaceRoot: root, Params: map[string]any{"table": "forecast", "unique_by": []any{"date"}}}

	first, err := executeBuiltinStoreAppend(t.Context(), withInput(base, map[string]core.Ref{
		"rows": {Inline: []map[string]any{
			{"date": "2026-06-24", "temp_max": "20"},
			{"date": "2026-06-25", "temp_max": "22"},
		}},
		"headers": {Inline: []string{"date", "temp_max"}},
	}), nil)
	if err != nil || first.Status != core.StatusOK {
		t.Fatalf("first save: status=%q err=%v %+v", first.Status, err, first.Error)
	}

	// Re-run: an overlapping date with a new value, plus a brand-new date.
	second, err := executeBuiltinStoreAppend(t.Context(), withInput(base, map[string]core.Ref{
		"rows": {Inline: []map[string]any{
			{"date": "2026-06-25", "temp_max": "99"}, // updates the existing row
			{"date": "2026-06-26", "temp_max": "18"}, // inserts a new one
		}},
		"headers": {Inline: []string{"date", "temp_max"}},
	}), nil)
	if err != nil || second.Status != core.StatusOK {
		t.Fatalf("second save: status=%q err=%v %+v", second.Status, err, second.Error)
	}

	q, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "SELECT date, temp_max FROM forecast ORDER BY date"},
	}, nil)
	if err != nil || q.Status != core.StatusOK {
		t.Fatalf("query: status=%q err=%v %+v", q.Status, err, q.Error)
	}
	rows, _ := q.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (no duplicate date)", len(rows))
	}
	for _, r := range rows {
		if r["date"] == "2026-06-25" && r["temp_max"] != "99" {
			t.Errorf("2026-06-25 temp_max = %v, want 99 (updated in place)", r["temp_max"])
		}
	}
}

// TestBuiltinStore_AppendUniqueByRejectsExistingDuplicates verifies that
// switching a collection that already holds duplicate keys to unique_by fails
// loudly (can't build the unique index) instead of silently doing nothing.
func TestBuiltinStore_AppendUniqueByRejectsExistingDuplicates(t *testing.T) {
	root := t.TempDir()
	// Plain append: two rows share a date — a duplicate on the would-be key.
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "forecast"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"date": "2026-06-24", "temp_max": "20"},
				{"date": "2026-06-24", "temp_max": "21"},
			}},
			"headers": {Inline: []string{"date", "temp_max"}},
		},
	}, nil); err != nil {
		t.Fatalf("seed append: %v", err)
	}
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "forecast", "unique_by": []any{"date"}},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"date": "2026-06-24", "temp_max": "30"}}},
			"headers": {Inline: []string{"date", "temp_max"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "not_unique" {
		t.Fatalf("want not_unique error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// findRows runs the no-SQL reader and returns its rows, failing the test on
// any error/non-OK status.
func findRows(t *testing.T, root string, params map[string]any) []map[string]any {
	t.Helper()
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{WorkspaceRoot: root, Params: params}, nil)
	if err != nil {
		t.Fatalf("find execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("find status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("find rows wrong type: %T", res.Output["rows"].Inline)
	}
	return rows
}

// TestBuiltinStore_Find exercises the no-code reader: filter (CEL from the
// row-condition builder), in-memory numeric sort, and limit — all without SQL.
func TestBuiltinStore_Find(t *testing.T) {
	root := t.TempDir()
	appendRes, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "invoices"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"customer": "Alice", "status": "unpaid", "amount": "120"},
				{"customer": "Bob", "status": "paid", "amount": "40"},
				{"customer": "Carol", "status": "unpaid", "amount": "9"},
			}},
			"headers": {Inline: []string{"customer", "status", "amount"}},
		},
	}, nil)
	if err != nil || appendRes.Status != core.StatusOK {
		t.Fatalf("append: status=%q err=%v %+v", appendRes.Status, err, appendRes.Error)
	}

	// No filter → every row.
	if got := findRows(t, root, map[string]any{"table": "invoices"}); len(got) != 3 {
		t.Fatalf("no filter: got %d rows, want 3", len(got))
	}

	// Filter: only unpaid invoices (CEL the builder would emit).
	unpaid := findRows(t, root, map[string]any{
		"table":  "invoices",
		"filter": `row.status == "unpaid"`,
	})
	if len(unpaid) != 2 {
		t.Fatalf("unpaid filter: got %d rows, want 2", len(unpaid))
	}

	// Numeric filter + descending numeric sort: amount > 50 then by amount.
	// Stored as TEXT, so this also proves numeric (not lexical) ordering.
	big := findRows(t, root, map[string]any{
		"table":    "invoices",
		"filter":   `double(row.amount) > 50`,
		"sort_by":  "amount",
		"sort_dir": "desc",
	})
	if len(big) != 1 || big[0]["customer"] != "Alice" {
		t.Fatalf("amount>50 desc: got %+v, want [Alice]", big)
	}

	// Sort ascending by amount with a limit: "9" must come before "40"
	// numerically (lexical would put "120" before "40").
	cheapest := findRows(t, root, map[string]any{
		"table":    "invoices",
		"sort_by":  "amount",
		"sort_dir": "asc",
		"limit":    2,
	})
	if len(cheapest) != 2 {
		t.Fatalf("limit: got %d rows, want 2", len(cheapest))
	}
	if cheapest[0]["customer"] != "Carol" || cheapest[1]["customer"] != "Bob" {
		t.Errorf("numeric asc order wrong: %v, %v", cheapest[0]["customer"], cheapest[1]["customer"])
	}
}

// TestBuiltinStore_FindCollectionFromInput verifies the Collection input
// overrides the table param: a name wired in wins over the dropdown value.
func TestBuiltinStore_FindCollectionFromInput(t *testing.T) {
	root := t.TempDir()
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"name": "Alice"}, {"name": "Bob"}}},
			"headers": {Inline: []string{"name"}},
		},
	}, nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Param points at a non-existent collection; the wired input names the real
	// one. Getting 2 rows proves the input won.
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "does-not-exist"},
		Input:         map[string]core.Ref{"table": {Inline: "leads"}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("find status=%q err=%v %+v", res.Status, err, res.Error)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Fatalf("wired collection input: got %d rows, want 2", len(rows))
	}
}

// TestBuiltinStore_FindMissingCollection verifies that reading a collection
// that doesn't exist fails loudly (rather than a silent empty result) so a
// typo'd name surfaces instead of doing nothing.
func TestBuiltinStore_FindMissingCollection(t *testing.T) {
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "nope"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "no_such_collection" {
		t.Fatalf("missing collection: want no_such_collection error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_FindExistingCollectionNoMatches verifies the kept-empty
// case: an EXISTING collection where no row matches the filter is a valid
// empty result, not an error.
func TestBuiltinStore_FindExistingCollectionNoMatches(t *testing.T) {
	root := t.TempDir()
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"status": "new"}}},
			"headers": {Inline: []string{"status"}},
		},
	}, nil); err != nil {
		t.Fatalf("append: %v", err)
	}
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads", "filter": `row.status == "archived"`},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%v %+v", res.Status, err, res.Error)
	}
	if rows, _ := res.Output["rows"].Inline.([]map[string]any); len(rows) != 0 {
		t.Fatalf("no matches: got %d rows, want 0 (empty OK, not error)", len(rows))
	}
}

// TestBuiltinStore_AppendEmptyBody verifies the empty-webhook-body path:
// a webhook trigger that fires with no request body emits "" on
// webhook_input.body; wired straight into a store's rows port that has
// historically produced a JSON parse error. The store should accept it
// as "nothing to insert" — same shape any non-techie hits when their
// form tool sends a heartbeat or a misconfigured caller fires empty.
func TestBuiltinStore_AppendEmptyBody(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows": {Inline: ""},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if got, _ := res.Output["inserted"].Inline.(int); got != 0 {
		t.Errorf("inserted = %v, want 0", res.Output["inserted"].Inline)
	}
}

// TestBuiltinStore_QueryEmptyStore verifies that reading before anything
// has ever been written returns an empty result, not an error — an
// empty store is a valid state.
func TestBuiltinStore_QueryEmptyStore(t *testing.T) {
	res, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"sql": "SELECT * FROM anything"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok || len(rows) != 0 {
		t.Errorf("expected empty rows, got %#v", res.Output["rows"].Inline)
	}
}

// TestBuiltinStore_AppendRequiresSandbox guards the no_sandbox path —
// the store can't function without a workspace root.
func TestBuiltinStore_AppendRequiresSandbox(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		Params: map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"name": "Alice"}}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error", res.Status)
	}
}

// seedStore writes the given rows into a collection in a fresh built-in store
// and returns the workspace root.
func seedStore(t *testing.T, table string, headers []string, rows []map[string]any) string {
	t.Helper()
	root := t.TempDir()
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": table},
		Input: map[string]core.Ref{
			"rows":    {Inline: rows},
			"headers": {Inline: headers},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return root
}

// TestBuiltinStore_QueryTooManyRows covers the row-ceiling guard in the query
// reader: with the ceiling lowered to 1, a 2-row uncapped SELECT trips it.
func TestBuiltinStore_QueryTooManyRows(t *testing.T) {
	root := seedStore(t, "leads", []string{"name"}, []map[string]any{{"name": "a"}, {"name": "b"}})
	restore := limits.SetMaxRows(1)
	defer restore()
	res, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "SELECT name FROM leads"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "too_many_rows" {
		t.Fatalf("want too_many_rows, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_FindTooManyRows covers the scan-bound guard in the find
// reader.
func TestBuiltinStore_FindTooManyRows(t *testing.T) {
	root := seedStore(t, "leads", []string{"name"}, []map[string]any{{"name": "a"}, {"name": "b"}})
	restore := limits.SetMaxRows(1)
	defer restore()
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "too_many_rows" {
		t.Fatalf("want too_many_rows, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_FindMissingCollectionLists covers the find reader's
// "no such table" branch on a store that DOES have other collections, so the
// error lists the available ones.
func TestBuiltinStore_FindMissingCollectionLists(t *testing.T) {
	root := seedStore(t, "leads", []string{"name"}, []map[string]any{{"name": "a"}})
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "does_not_exist"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "no_such_collection" {
		t.Fatalf("want no_such_collection, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestParseUniqueBy covers the optional-key parsing: absent, empty, valid,
// invalid identifier, and not-a-saved-column.
func TestParseUniqueBy(t *testing.T) {
	t.Run("absent → nil, no error", func(t *testing.T) {
		keys, errRes := parseUniqueBy(core.Job{Params: map[string]any{}}, []string{"date"})
		if errRes != nil || keys != nil {
			t.Fatalf("got (%v, %+v)", keys, errRes)
		}
	})
	t.Run("nil value → nil, no error", func(t *testing.T) {
		keys, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": nil}}, []string{"date"})
		if errRes != nil || keys != nil {
			t.Fatalf("got (%v, %+v)", keys, errRes)
		}
	})
	t.Run("empty array → nil, no error", func(t *testing.T) {
		keys, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": []any{}}}, []string{"date"})
		if errRes != nil || keys != nil {
			t.Fatalf("got (%v, %+v)", keys, errRes)
		}
	})
	t.Run("valid key in headers", func(t *testing.T) {
		keys, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": []any{"date"}}}, []string{"date", "temp"})
		if errRes != nil || len(keys) != 1 || keys[0] != "date" {
			t.Fatalf("got (%v, %+v)", keys, errRes)
		}
	})
	t.Run("wrong element type errors", func(t *testing.T) {
		_, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": "notarray"}}, nil)
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("invalid identifier errors", func(t *testing.T) {
		// validateIdent rejects a NUL byte (would terminate the C-string most
		// drivers pass to the database).
		bad := "bad\x00col"
		_, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": []any{bad}}}, []string{bad})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("key not among saved columns errors", func(t *testing.T) {
		_, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": []any{"missing"}}}, []string{"date"})
		if errRes == nil || errRes.Error.Code != "bad_param" {
			t.Fatalf("want bad_param, got %+v", errRes)
		}
	})
	t.Run("no headers skips membership check", func(t *testing.T) {
		// With no headers (empty body), a valid identifier is accepted even
		// though it can't be checked against columns yet.
		keys, errRes := parseUniqueBy(core.Job{Params: map[string]any{"unique_by": []any{"date"}}}, nil)
		if errRes != nil || len(keys) != 1 {
			t.Fatalf("got (%v, %+v)", keys, errRes)
		}
	})
}

// TestBuiltinStore_AppendBadTableName covers the validateIdent rejection of an
// unsafe collection name.
func TestBuiltinStore_AppendBadTableName(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "bad\x00tbl"},
		Input:         map[string]core.Ref{"rows": {Inline: []map[string]any{{"x": 1}}}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_AppendMissingRowsInput covers the required-input guard.
func TestBuiltinStore_AppendMissingRowsInput(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "leads"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Fatalf("want missing_input, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_AppendBadColumnName covers the per-header identifier check.
func TestBuiltinStore_AppendBadColumnName(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"bad\x00col": 1}}},
			"headers": {Inline: []string{"bad\x00col"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_AppendBadColumnType covers the column_types validation
// boundary (an unsafe type string is rejected as a db error).
func TestBuiltinStore_AppendBadColumnType(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params: map[string]any{
			"table":        "leads",
			"column_types": map[string]any{"id": "INTEGER; DROP TABLE x"},
		},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"id": 1}}},
			"headers": {Inline: []string{"id"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "db" {
		t.Fatalf("want db error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_AppendBadInputShape covers the normalizeRows error path
// (an unsupported scalar input that isn't an empty string or object).
func TestBuiltinStore_AppendBadInputShape(t *testing.T) {
	res, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "leads"},
		Input:         map[string]core.Ref{"rows": {Inline: 42}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Fatalf("want bad_input, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_QueryMissingSQL / EmptySQL / BadParams cover the param
// validation in the query reader before any file is opened.
func TestBuiltinStore_QueryParamErrors(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing sql", map[string]any{}},
		{"empty sql", map[string]any{"sql": ""}},
		{"params not array", map[string]any{"sql": "SELECT 1", "params": "nope"}},
		{"negative limit", map[string]any{"sql": "SELECT 1", "limit": -2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := executeBuiltinStoreQuery(t.Context(), core.Job{
				WorkspaceRoot: root,
				Params:        c.params,
			}, nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Status != core.StatusError || res.Error.Code != "bad_param" {
				t.Fatalf("want bad_param, got status=%q err=%+v", res.Status, res.Error)
			}
		})
	}
}

// TestBuiltinStore_QueryBadSQL covers the db-error path on a real store.
func TestBuiltinStore_QueryBadSQL(t *testing.T) {
	root := t.TempDir()
	// Materialize the store with one collection.
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"x": "1"}}},
			"headers": {Inline: []string{"x"}},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"sql": "SELECT * FROM no_such_table_zzz"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error.Code != "db" {
		t.Fatalf("want db error, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_QueryWithParamsAndLimit exercises the placeholder + limit
// path of the query reader against real data.
func TestBuiltinStore_QueryWithParamsAndLimit(t *testing.T) {
	root := t.TempDir()
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"name": "Alice", "tier": "gold"},
				{"name": "Bob", "tier": "gold"},
				{"name": "Carol", "tier": "silver"},
			}},
			"headers": {Inline: []string{"name", "tier"}},
		},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := executeBuiltinStoreQuery(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params: map[string]any{
			"sql":    "SELECT name FROM leads WHERE tier = ? ORDER BY name",
			"params": []any{"gold"},
			"limit":  1,
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, _ := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "Alice" {
		t.Fatalf("rows = %+v, want just Alice (limit)", rows)
	}
}

// TestOpenBuiltinStore_NoSandbox covers the missing-workspace guard.
func TestOpenBuiltinStore_NoSandbox(t *testing.T) {
	db, errRes := openBuiltinStore(core.Job{}, false)
	if db != nil {
		t.Fatal("want nil db")
	}
	if errRes == nil || errRes.Error.Code != "no_sandbox" {
		t.Fatalf("want no_sandbox, got %+v", errRes)
	}
}

// TestOpenBuiltinStore_ReadMissingIsEmpty covers the read-path where no file
// exists yet: nil db, nil result (an empty store, not an error).
func TestOpenBuiltinStore_ReadMissingIsEmpty(t *testing.T) {
	db, errRes := openBuiltinStore(core.Job{WorkspaceRoot: t.TempDir()}, false)
	if db != nil || errRes != nil {
		t.Fatalf("want (nil, nil), got (%v, %+v)", db, errRes)
	}
}
