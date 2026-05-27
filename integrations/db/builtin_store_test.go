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
