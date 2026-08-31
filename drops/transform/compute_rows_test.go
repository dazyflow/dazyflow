// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// runCompute is the compute_rows analog of map_rows' run helper —
// keeps the test bodies focused on the case-under-test.
func runCompute(t *testing.T, params map[string]any, rows []map[string]any, headers []string) ([]map[string]any, []string) {
	t.Helper()
	input := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		input["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeComputeRows(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["rows"].Inline.([]map[string]any),
		res.Output["rows"].Headers
}

func TestComputeRows_StringConcat(t *testing.T) {
	rows, headers := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"full_name": "row.first_name + ' ' + row.last_name",
			},
		},
		[]map[string]any{
			{"first_name": "Alice", "last_name": "Walker"},
			{"first_name": "Bob", "last_name": "King"},
		},
		[]string{"first_name", "last_name"})
	if !reflect.DeepEqual(headers, []string{"first_name", "last_name", "full_name"}) {
		t.Errorf("headers = %v", headers)
	}
	if rows[0]["full_name"] != "Alice Walker" || rows[1]["full_name"] != "Bob King" {
		t.Errorf("rows = %+v", rows)
	}
	// Original columns preserved.
	if rows[0]["first_name"] != "Alice" {
		t.Errorf("first_name dropped: %+v", rows[0])
	}
}

func TestComputeRows_Arithmetic(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"total": "row.qty * row.price",
			},
		},
		[]map[string]any{{"qty": int64(3), "price": int64(10)}},
		nil)
	if rows[0]["total"] != int64(30) {
		t.Errorf("total = %v (%T), want int64(30)", rows[0]["total"], rows[0]["total"])
	}
}

func TestComputeRows_ConditionalExpression(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"tier": "row.score > 90 ? 'gold' : row.score > 70 ? 'silver' : 'bronze'",
			},
		},
		[]map[string]any{
			{"name": "A", "score": int64(95)},
			{"name": "B", "score": int64(80)},
			{"name": "C", "score": int64(60)},
		},
		nil)
	if rows[0]["tier"] != "gold" || rows[1]["tier"] != "silver" || rows[2]["tier"] != "bronze" {
		t.Errorf("tiers = %v / %v / %v", rows[0]["tier"], rows[1]["tier"], rows[2]["tier"])
	}
}

func TestComputeRows_BooleanResultAsCell(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"is_adult": "row.age >= 18",
			},
		},
		[]map[string]any{
			{"name": "A", "age": int64(30)},
			{"name": "B", "age": int64(12)},
		},
		nil)
	if rows[0]["is_adult"] != true || rows[1]["is_adult"] != false {
		t.Errorf("is_adult = %v / %v", rows[0]["is_adult"], rows[1]["is_adult"])
	}
}

func TestComputeRows_OverwriteExistingColumn(t *testing.T) {
	// "name" already exists; compute overrides it. Output headers
	// should NOT duplicate "name".
	rows, headers := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"name": "row.name + '!'",
			},
		},
		[]map[string]any{{"name": "Alice"}},
		[]string{"name"})
	if !reflect.DeepEqual(headers, []string{"name"}) {
		t.Errorf("headers = %v, want [name] (no duplicate)", headers)
	}
	if rows[0]["name"] != "Alice!" {
		t.Errorf("name = %v, want 'Alice!'", rows[0]["name"])
	}
}

func TestComputeRows_MultipleComputeOrderIsAlphabetical(t *testing.T) {
	// Compute keys appear in the output headers in alphabetical
	// order — documented behavior so tests don't flake on Go's
	// random map iteration.
	_, headers := runCompute(t,
		map[string]any{
			"compute": map[string]any{
				"zeta":  "1",
				"alpha": "2",
				"mango": "3",
			},
		},
		[]map[string]any{{"existing": "x"}},
		[]string{"existing"})
	want := []string{"existing", "alpha", "mango", "zeta"}
	if !reflect.DeepEqual(headers, want) {
		t.Errorf("headers = %v, want %v", headers, want)
	}
}

func TestComputeRows_Filter(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{
			"filter": "row.active == true && row.score >= 70",
		},
		[]map[string]any{
			{"name": "A", "active": true, "score": int64(80)},
			{"name": "B", "active": true, "score": int64(50)},
			{"name": "C", "active": false, "score": int64(95)},
			{"name": "D", "active": true, "score": int64(95)},
		},
		nil)
	if len(rows) != 2 || rows[0]["name"] != "A" || rows[1]["name"] != "D" {
		t.Errorf("rows = %+v, want A and D", rows)
	}
}

func TestComputeRows_FilterAndCompute(t *testing.T) {
	// Real ETL shape: drop the noise, then derive a column.
	rows, headers := runCompute(t,
		map[string]any{
			"filter": "row.status == 'active'",
			"compute": map[string]any{
				"display_name": "row.first_name + ' ' + row.last_name",
			},
		},
		[]map[string]any{
			{"id": int64(1), "first_name": "Alice", "last_name": "Walker", "status": "active"},
			{"id": int64(2), "first_name": "Bob", "last_name": "King", "status": "deleted"},
			{"id": int64(3), "first_name": "Carol", "last_name": "Day", "status": "active"},
		},
		[]string{"id", "first_name", "last_name", "status"})
	if len(rows) != 2 || rows[0]["display_name"] != "Alice Walker" || rows[1]["display_name"] != "Carol Day" {
		t.Errorf("rows = %+v", rows)
	}
	want := []string{"id", "first_name", "last_name", "status", "display_name"}
	if !reflect.DeepEqual(headers, want) {
		t.Errorf("headers = %v, want %v", headers, want)
	}
}

func TestComputeRows_Identity(t *testing.T) {
	rows, headers := runCompute(t, nil,
		[]map[string]any{{"a": "1", "b": "2"}},
		[]string{"a", "b"})
	if !reflect.DeepEqual(headers, []string{"a", "b"}) {
		t.Errorf("headers = %v", headers)
	}
	if rows[0]["a"] != "1" || rows[0]["b"] != "2" {
		t.Errorf("rows = %+v", rows)
	}
}

// --- Error cases ------------------------------------------------------

func TestComputeRows_BadSyntaxIsCompileError(t *testing.T) {
	// Syntax error in the expression must fail before the row loop —
	// no partial output, no per-row scan.
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{
			"compute": map[string]any{"x": "row.a +"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": int64(1)}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestComputeRows_FilterMustReturnBool(t *testing.T) {
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "row.a + 1"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": int64(1)}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status=%q code=%q, want eval", res.Status, res.Error.Code)
	}
}

func TestComputeRows_RuntimeErrorFailsBatch(t *testing.T) {
	// Referring to a field that doesn't exist on a row is a CEL
	// runtime error — we fail the whole batch rather than emitting
	// partial output, mirroring the SQL drops' all-or-nothing
	// contract.
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{
			"compute": map[string]any{"x": "row.missing_field + 1"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{
				{"a": int64(1)}, // missing_field is absent → runtime error
			}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status=%q code=%q, want eval", res.Status, res.Error.Code)
	}
}

func TestComputeRows_NonStringExpressionRejected(t *testing.T) {
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{
			"compute": map[string]any{"x": 42}, // should be a string expression
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": int64(1)}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestComputeRows_MissingRowsInput(t *testing.T) {
	res, _ := executeComputeRows(t.Context(), core.Job{Params: map[string]any{}}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

// --- Shape + composition ----------------------------------------------

func TestComputeRows_JSONRoundtripShape(t *testing.T) {
	// gRPC/MCP roundtrip: rows arrive as []any of map[string]any.
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{
			"compute": map[string]any{"doubled": "row.n * 2"},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"n": int64(3)},
				map[string]any{"n": int64(7)},
			}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if rows[0]["doubled"] != int64(6) || rows[1]["doubled"] != int64(14) {
		t.Errorf("rows = %+v", rows)
	}
}

func TestComputeRows_PreservesInputRowReference(t *testing.T) {
	// The drop should not mutate the input row map — downstream
	// nodes might share it. Verify by inspecting the input slice
	// after the call.
	input := []map[string]any{{"a": int64(1)}}
	res, _ := executeComputeRows(t.Context(), core.Job{
		Params: map[string]any{"compute": map[string]any{"b": "row.a + 10"}},
		Input:  map[string]core.Ref{"rows": {Inline: input}},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	if _, ok := input[0]["b"]; ok {
		t.Errorf("input row was mutated: %+v", input[0])
	}
}

func TestComputeRows_EmptyRows(t *testing.T) {
	rows, headers := runCompute(t,
		map[string]any{"compute": map[string]any{"x": "1"}},
		[]map[string]any{},
		[]string{"a", "b"})
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
	want := []string{"a", "b", "x"}
	if !reflect.DeepEqual(headers, want) {
		t.Errorf("headers = %v, want %v", headers, want)
	}
}

func TestComputeRows_FilterMatchesNothing(t *testing.T) {
	rows, _ := runCompute(t,
		map[string]any{"filter": "row.score > 9000"},
		[]map[string]any{{"score": int64(5)}, {"score": int64(50)}},
		nil)
	if len(rows) != 0 {
		t.Errorf("rows = %+v, want empty", rows)
	}
}

// A computed column whose formula returns an object must come out as a plain
// JSON map, not cel-go's map[any]any (which no downstream step accepts).
func TestComputeRows_ObjectValuedColumnIsJSONShaped(t *testing.T) {
	res, err := executeComputeRows(t.Context(), core.Job{
		ID:     "test",
		Params: map[string]any{"compute": map[string]any{"contact": "{'email': row.email}"}},
		Input:  map[string]core.Ref{"rows": {Inline: []any{map[string]any{"email": "a@x.se"}}}},
	}, nil)
	if err != nil || res.Status != core.StatusOK {
		t.Fatalf("status = %v err = %v error = %+v", res.Status, err, res.Error)
	}
	if _, err := json.Marshal(res.Output["rows"].Inline); err != nil {
		t.Fatalf("computed rows are not JSON-serialisable: %v", err)
	}
}
