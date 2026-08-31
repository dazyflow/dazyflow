// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// TestCellFloat covers every numeric type a SQLite scan or TEXT value can
// carry, plus the non-numeric fallthrough.
func TestCellFloat(t *testing.T) {
	cases := []struct {
		name   string
		v      any
		want   float64
		wantOK bool
	}{
		{"float64", float64(3.5), 3.5, true},
		{"int64", int64(7), 7, true},
		{"int", 9, 9, true},
		{"numeric string", " 42 ", 42, true},
		{"non-numeric string", "abc", 0, false},
		{"bool not numeric", true, 0, false},
		{"nil not numeric", nil, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := cellFloat(c.v)
			if ok != c.wantOK || (ok && got != c.want) {
				t.Errorf("cellFloat(%v) = (%v, %v), want (%v, %v)", c.v, got, ok, c.want, c.wantOK)
			}
		})
	}
}

// TestCellString covers nil → "", []byte → string, and the fmt.Sprint
// fallback for arbitrary values.
func TestCellString(t *testing.T) {
	if got := cellString(nil); got != "" {
		t.Errorf("nil = %q, want empty", got)
	}
	if got := cellString([]byte("hi")); got != "hi" {
		t.Errorf("[]byte = %q, want hi", got)
	}
	if got := cellString(int64(42)); got != "42" {
		t.Errorf("int64 = %q, want 42", got)
	}
	if got := cellString("plain"); got != "plain" {
		t.Errorf("string = %q, want plain", got)
	}
}

// TestCellLess covers numeric ordering when both parse, lexical ordering
// otherwise, and the NULL-sorts-first behaviour.
func TestCellLess(t *testing.T) {
	// Numeric: "9" before "10" (lexical would invert).
	if !cellLess("9", "10") {
		t.Error(`"9" should sort before "10" numerically`)
	}
	if cellLess("10", "9") {
		t.Error(`"10" should not sort before "9"`)
	}
	// Mixed (one non-numeric) falls back to lexical.
	if !cellLess("apple", "banana") {
		t.Error("lexical: apple < banana")
	}
	// nil sorts as empty string, before any non-empty value.
	if !cellLess(nil, "x") {
		t.Error("nil should sort before a non-empty string")
	}
	// Numeric vs non-numeric: lexical compare of string forms.
	if cellLess("zebra", "5") {
		t.Error(`"zebra" should not sort before "5" lexically`)
	}
}

// TestResolveTable covers the param/input precedence and both error paths.
func TestResolveTable(t *testing.T) {
	t.Run("param used when no input", func(t *testing.T) {
		got, err := resolveTable(core.Job{Params: map[string]any{"table": "leads"}})
		if err != nil || got != "leads" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("wired input wins over param", func(t *testing.T) {
		got, err := resolveTable(core.Job{
			Params: map[string]any{"table": "param_tbl"},
			Input:  map[string]core.Ref{"table": {Inline: "input_tbl"}},
		})
		if err != nil || got != "input_tbl" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("blank input falls back to param", func(t *testing.T) {
		got, err := resolveTable(core.Job{
			Params: map[string]any{"table": "param_tbl"},
			Input:  map[string]core.Ref{"table": {Inline: "  "}},
		})
		if err != nil || got != "param_tbl" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("non-text input errors", func(t *testing.T) {
		_, err := resolveTable(core.Job{
			Input: map[string]core.Ref{"table": {Inline: 42}},
		})
		if err == nil || !strings.Contains(err.Error(), "must be text") {
			t.Errorf("err = %v, want must-be-text", err)
		}
	})
	t.Run("nothing set errors", func(t *testing.T) {
		_, err := resolveTable(core.Job{})
		if err == nil || !strings.Contains(err.Error(), "pick a collection") {
			t.Errorf("err = %v, want pick-a-collection", err)
		}
	})
}

// TestMissingCollectionMsg_AndExistingCollections covers the listing helpers
// through a real built-in store: one with collections and one empty.
func TestMissingCollectionMsg_AndExistingCollections(t *testing.T) {
	root := t.TempDir()
	// Create two collections in the built-in store.
	for _, tbl := range []string{"invoices", "leads"} {
		if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
			WorkspaceRoot: root,
			Params:        map[string]any{"table": tbl},
			Input: map[string]core.Ref{
				"rows":    {Inline: []map[string]any{{"x": "1"}}},
				"headers": {Inline: []string{"x"}},
			},
		}, nil); err != nil {
			t.Fatalf("seed %s: %v", tbl, err)
		}
	}

	db, errRes := openBuiltinStore(core.Job{WorkspaceRoot: root}, false)
	if errRes != nil || db == nil {
		t.Fatalf("openBuiltinStore: %+v", errRes)
	}
	defer db.Close()

	names := existingCollections(t.Context(), db)
	if len(names) != 2 || names[0] != "invoices" || names[1] != "leads" {
		t.Fatalf("existingCollections = %v, want [invoices leads]", names)
	}

	msg := missingCollectionMsg(t.Context(), db, "typo")
	if !strings.Contains(msg, "available collections") || !strings.Contains(msg, "invoices") {
		t.Errorf("msg = %q, want it to list available collections", msg)
	}
}

// TestMissingCollectionMsg_EmptyStore covers the no-collections branch.
func TestMissingCollectionMsg_EmptyStore(t *testing.T) {
	root := t.TempDir()
	// Create the store file with no user tables by appending an empty body.
	if _, err := executeBuiltinStoreAppend(t.Context(), core.Job{
		WorkspaceRoot: root,
		Params:        map[string]any{"table": "leads"},
		Input:         map[string]core.Ref{"rows": {Inline: ""}},
	}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	db, errRes := openBuiltinStore(core.Job{WorkspaceRoot: root}, false)
	if errRes != nil || db == nil {
		t.Fatalf("openBuiltinStore: %+v", errRes)
	}
	defer db.Close()
	msg := missingCollectionMsg(t.Context(), db, "nope")
	if !strings.Contains(msg, "no collections have been created yet") {
		t.Errorf("msg = %q, want empty-store wording", msg)
	}
}

// TestBuiltinStore_FindNegativeLimit covers the negative-limit guard in the
// find reader.
func TestBuiltinStore_FindNegativeLimit(t *testing.T) {
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "x", "limit": -1},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_FindBadFilter covers the CEL compile-error path.
func TestBuiltinStore_FindBadFilter(t *testing.T) {
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Params:        map[string]any{"table": "x", "filter": "this is not (valid CEL"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param for bad filter, got status=%q err=%+v", res.Status, res.Error)
	}
}

// TestBuiltinStore_FindBadTableParam covers the resolveTable error surfaced as
// a bad_param result.
func TestBuiltinStore_FindBadTableParam(t *testing.T) {
	res, err := executeBuiltinStoreFind(t.Context(), core.Job{
		WorkspaceRoot: t.TempDir(),
		Input:         map[string]core.Ref{"table": {Inline: 99}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusError || res.Error == nil || res.Error.Code != "bad_param" {
		t.Fatalf("want bad_param, got status=%q err=%+v", res.Status, res.Error)
	}
}
