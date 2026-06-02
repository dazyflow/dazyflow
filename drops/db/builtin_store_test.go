package db

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
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
