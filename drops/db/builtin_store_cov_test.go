// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/limits"
)

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
