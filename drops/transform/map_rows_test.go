// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"reflect"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// run is a small wrapper that exercises executeMapRows with the
// common shape — params + rows + optional headers — and returns the
// output rows + headers, fatal'ing on any error. Test bodies stay
// close to the test cases that way.
func run(t *testing.T, params map[string]any, rows []map[string]any, headers []string) (outRows []map[string]any, outHeaders []string) {
	t.Helper()
	input := map[string]core.Ref{
		"rows": {Inline: rows},
	}
	if headers != nil {
		input["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeMapRows(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["rows"].Inline.([]map[string]any),
		res.Output["rows"].Headers
}

func TestMapRows_Identity(t *testing.T) {
	// No params → output equals input (rows + headers).
	rows, headers := run(t, nil,
		[]map[string]any{{"a": "1", "b": "2"}, {"a": "3", "b": "4"}},
		[]string{"a", "b"})
	if !reflect.DeepEqual(headers, []string{"a", "b"}) {
		t.Errorf("headers = %v", headers)
	}
	if len(rows) != 2 || rows[0]["a"] != "1" || rows[1]["b"] != "4" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapRows_Select(t *testing.T) {
	rows, headers := run(t,
		map[string]any{"select": []string{"name"}},
		[]map[string]any{
			{"id": 1, "name": "Alice", "age": 30},
			{"id": 2, "name": "Bob", "age": 25},
		},
		[]string{"id", "name", "age"})
	if !reflect.DeepEqual(headers, []string{"name"}) {
		t.Errorf("headers = %v, want [name]", headers)
	}
	if _, ok := rows[0]["id"]; ok {
		t.Errorf("id leaked: %+v", rows[0])
	}
	if rows[0]["name"] != "Alice" || rows[1]["name"] != "Bob" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapRows_Drop(t *testing.T) {
	rows, headers := run(t,
		map[string]any{"drop": []string{"internal_id"}},
		[]map[string]any{{"internal_id": "x", "name": "Alice"}},
		[]string{"internal_id", "name"})
	if !reflect.DeepEqual(headers, []string{"name"}) {
		t.Errorf("headers = %v", headers)
	}
	if _, ok := rows[0]["internal_id"]; ok {
		t.Errorf("internal_id leaked: %+v", rows[0])
	}
}

func TestMapRows_SelectAndDropMutuallyExclusive(t *testing.T) {
	res, _ := executeMapRows(t.Context(), core.Job{
		Params: map[string]any{
			"select": []string{"a"},
			"drop":   []string{"b"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1", "b": "2"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestMapRows_Rename(t *testing.T) {
	rows, headers := run(t,
		map[string]any{"rename": map[string]any{"first_name": "name", "yrs": "age"}},
		[]map[string]any{{"first_name": "Alice", "yrs": 30}},
		[]string{"first_name", "yrs"})
	if !reflect.DeepEqual(headers, []string{"name", "age"}) {
		t.Errorf("headers = %v, want [name age]", headers)
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != 30 {
		t.Errorf("rows = %+v", rows)
	}
	if _, ok := rows[0]["first_name"]; ok {
		t.Errorf("first_name leaked: %+v", rows[0])
	}
}

func TestMapRows_SelectThenRename(t *testing.T) {
	// select uses INPUT names; rename happens after, so output uses
	// the renamed names. This is the "match columns to DB schema"
	// flow that motivated the drop.
	rows, headers := run(t,
		map[string]any{
			"select": []string{"first_name", "age"},
			"rename": map[string]any{"first_name": "name"},
		},
		[]map[string]any{{"id": "x", "first_name": "Alice", "age": 30}},
		[]string{"id", "first_name", "age"})
	if !reflect.DeepEqual(headers, []string{"name", "age"}) {
		t.Errorf("headers = %v, want [name age]", headers)
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != 30 {
		t.Errorf("rows = %+v", rows)
	}
}

func TestMapRows_DefaultFillsMissingAndNull(t *testing.T) {
	rows, _ := run(t,
		map[string]any{"default": map[string]any{
			"name":   "unknown",
			"active": true,
		}},
		[]map[string]any{
			{"name": "Alice", "active": false}, // both present
			{"name": nil, "active": true},      // name explicitly null
			{"active": true},                   // name missing
		},
		[]string{"name", "active"})
	if rows[0]["name"] != "Alice" || rows[0]["active"] != false {
		t.Errorf("row 0 should be untouched: %+v", rows[0])
	}
	if rows[1]["name"] != "unknown" {
		t.Errorf("row 1 nil should be defaulted: %+v", rows[1])
	}
	if rows[2]["name"] != "unknown" {
		t.Errorf("row 2 missing should be defaulted: %+v", rows[2])
	}
}

func TestMapRows_DefaultUsesInputColumnName(t *testing.T) {
	// default refers to INPUT name; rename applies after default fills.
	rows, _ := run(t,
		map[string]any{
			"default": map[string]any{"first_name": "anon"},
			"rename":  map[string]any{"first_name": "name"},
		},
		[]map[string]any{{}},
		[]string{"first_name"})
	if rows[0]["name"] != "anon" {
		t.Errorf("rows[0].name = %v, want 'anon'", rows[0]["name"])
	}
}

func TestMapRows_FilterEq(t *testing.T) {
	rows, _ := run(t,
		map[string]any{"filter_eq": map[string]any{"status": "active"}},
		[]map[string]any{
			{"name": "A", "status": "active"},
			{"name": "B", "status": "disabled"},
			{"name": "C", "status": "active"},
		},
		nil)
	if len(rows) != 2 || rows[0]["name"] != "A" || rows[1]["name"] != "C" {
		t.Errorf("rows = %+v, want A and C", rows)
	}
}

func TestMapRows_FilterEqStringComparisonLenient(t *testing.T) {
	// int 30 should match the string "30" — Excel rows arrive as
	// strings, typed DB rows as ints, users almost never want the
	// strict-equality footgun.
	rows, _ := run(t,
		map[string]any{"filter_eq": map[string]any{"age": 30}},
		[]map[string]any{
			{"name": "A", "age": "30"}, // string from Excel
			{"name": "B", "age": 25},   // int from DB
			{"name": "C", "age": 30},   // int matches
		},
		nil)
	if len(rows) != 2 || rows[0]["name"] != "A" || rows[1]["name"] != "C" {
		t.Errorf("rows = %+v, want A (string-30) and C (int-30)", rows)
	}
}

func TestMapRows_FilterNeq(t *testing.T) {
	rows, _ := run(t,
		map[string]any{"filter_neq": map[string]any{"status": "deleted"}},
		[]map[string]any{
			{"name": "A", "status": "active"},
			{"name": "B", "status": "deleted"},
		},
		nil)
	if len(rows) != 1 || rows[0]["name"] != "A" {
		t.Errorf("rows = %+v, want only A", rows)
	}
}

func TestMapRows_FilterIn(t *testing.T) {
	rows, _ := run(t,
		map[string]any{"filter_in": map[string]any{
			"plan": []any{"pro", "enterprise"},
		}},
		[]map[string]any{
			{"name": "A", "plan": "free"},
			{"name": "B", "plan": "pro"},
			{"name": "C", "plan": "enterprise"},
		},
		nil)
	if len(rows) != 2 || rows[0]["name"] != "B" || rows[1]["name"] != "C" {
		t.Errorf("rows = %+v, want B and C", rows)
	}
}

func TestMapRows_AllFiltersAreAND(t *testing.T) {
	// Multiple keys inside filter_eq AND together; multiple filter_*
	// keys also AND together.
	rows, _ := run(t,
		map[string]any{
			"filter_eq": map[string]any{"status": "active", "country": "SE"},
			"filter_in": map[string]any{"plan": []any{"pro", "enterprise"}},
		},
		[]map[string]any{
			{"id": 1, "status": "active", "country": "SE", "plan": "pro"},   // keep
			{"id": 2, "status": "active", "country": "NO", "plan": "pro"},   // wrong country
			{"id": 3, "status": "active", "country": "SE", "plan": "free"},  // wrong plan
			{"id": 4, "status": "disabled", "country": "SE", "plan": "pro"}, // wrong status
		},
		nil)
	if len(rows) != 1 || rows[0]["id"] != 1 {
		t.Errorf("rows = %+v, want only id=1", rows)
	}
}

func TestMapRows_FilterAndSelectCompose(t *testing.T) {
	// Real ETL shape: filter active users, then project the columns
	// our DB schema wants, then rename.
	rows, headers := run(t,
		map[string]any{
			"filter_eq": map[string]any{"status": "active"},
			"select":    []string{"id", "first_name", "email"},
			"rename":    map[string]any{"first_name": "name"},
		},
		[]map[string]any{
			{"id": 1, "first_name": "Alice", "email": "a@x", "status": "active", "noise": "yes"},
			{"id": 2, "first_name": "Bob", "email": "b@x", "status": "disabled"},
		},
		[]string{"id", "first_name", "email", "status", "noise"})
	if !reflect.DeepEqual(headers, []string{"id", "name", "email"}) {
		t.Errorf("headers = %v", headers)
	}
	if len(rows) != 1 || rows[0]["name"] != "Alice" {
		t.Errorf("rows = %+v", rows)
	}
	if _, ok := rows[0]["noise"]; ok {
		t.Errorf("noise leaked: %+v", rows[0])
	}
}

func TestMapRows_EmptyRows(t *testing.T) {
	rows, headers := run(t, map[string]any{"select": []string{"a"}},
		[]map[string]any{}, []string{"a", "b"})
	if !reflect.DeepEqual(headers, []string{"a"}) {
		t.Errorf("headers = %v, want [a]", headers)
	}
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

func TestMapRows_FilterMatchesNothing(t *testing.T) {
	rows, _ := run(t,
		map[string]any{"filter_eq": map[string]any{"status": "active"}},
		[]map[string]any{{"name": "A", "status": "disabled"}},
		nil)
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

func TestMapRows_MissingRowsInput(t *testing.T) {
	res, _ := executeMapRows(t.Context(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestMapRows_DerivedHeadersWhenNotProvided(t *testing.T) {
	// No headers input → derived alphabetically from row keys.
	_, headers := run(t, nil,
		[]map[string]any{{"zebra": "z", "apple": "a"}},
		nil)
	if !reflect.DeepEqual(headers, []string{"apple", "zebra"}) {
		t.Errorf("headers = %v, want sorted derivation", headers)
	}
}

func TestMapRows_JSONRoundtripShape(t *testing.T) {
	// gRPC/MCP path: rows arrive as []any of map[string]any.
	res, _ := executeMapRows(t.Context(), core.Job{
		Params: map[string]any{"select": []string{"a"}},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"a": "1", "b": "2"},
				map[string]any{"a": "3", "b": "4"},
			}},
			"headers": {Inline: []any{"a", "b"}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 || rows[0]["a"] != "1" || rows[1]["a"] != "3" {
		t.Errorf("rows = %+v", rows)
	}
}
