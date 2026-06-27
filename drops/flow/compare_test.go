// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// got reads the 1/0 Result the Compare drop emits, as a bool.
func got(t *testing.T, res core.Result) bool {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	// Compare emits an int (1/0); In Range emits a real bool. Accept both.
	switch v := res.Output["result"].Inline.(type) {
	case bool:
		return v
	case int:
		if v != 0 && v != 1 {
			t.Fatalf("result = %d, want 0 or 1", v)
		}
		return v == 1
	default:
		t.Fatalf("result port missing or not a bool/int: %#v", res.Output["result"].Inline)
		return false
	}
}

// cmp wires A and B as inputs and runs Compare with the given op.
func cmp(t *testing.T, a, b any, op string, extra map[string]any) core.Result {
	t.Helper()
	p := map[string]any{"op": op}
	for k, v := range extra {
		p[k] = v
	}
	res, _ := executeCompare(t.Context(), core.Job{
		Input:  map[string]core.Ref{"A": {Inline: a}, "B": {Inline: b}},
		Params: p,
	}, nil)
	return res
}

func TestCompare_Equals(t *testing.T) {
	if !got(t, cmp(t, "succeeded", "succeeded", "equals", nil)) {
		t.Errorf("expected 1 for equals match")
	}
	if got(t, cmp(t, "failed", "succeeded", "equals", nil)) {
		t.Errorf("expected 0 for equals mismatch")
	}
	if !got(t, cmp(t, "a", "b", "not_equals", nil)) {
		t.Errorf("expected 1 for not_equals")
	}
}

func TestCompare_ResultIsBool(t *testing.T) {
	res := cmp(t, 1.0, 1.0, "equals", nil)
	b, ok := res.Output["result"].Inline.(bool)
	if !ok {
		t.Fatalf("result must be a bool, got %T", res.Output["result"].Inline)
	}
	if !b {
		t.Errorf("1 equals 1 should be true")
	}
	if mime := res.Output["result"].MIME; mime != core.MIMEBool {
		t.Errorf("result MIME = %q, want %q", mime, core.MIMEBool)
	}
}

func TestCompare_NumericAllOps(t *testing.T) {
	for _, c := range []struct {
		op   string
		a, b float64
		want bool
	}{
		{"greater_than", 10001, 10000, true},
		{"greater_than", 10000, 10000, false},
		{"less_than", 5, 10, true},
		{"less_than", 10, 10, false},
		{"less_or_equal", 10, 10, true},
		{"less_or_equal", 11, 10, false},
		{"greater_or_equal", 10, 10, true},
		{"greater_or_equal", 9, 10, false},
	} {
		t.Run(c.op, func(t *testing.T) {
			if got(t, cmp(t, c.a, c.b, c.op, nil)) != c.want {
				t.Errorf("op=%s a=%v b=%v: want %v", c.op, c.a, c.b, c.want)
			}
		})
	}
}

func TestCompare_OneOf(t *testing.T) {
	set := []any{200.0, 201.0, 204.0}
	if !got(t, cmp(t, 201.0, set, "one_of", nil)) {
		t.Errorf("201 should be one_of the set")
	}
	if got(t, cmp(t, 500.0, set, "one_of", nil)) {
		t.Errorf("500 should not be one_of the set")
	}
	if !got(t, cmp(t, 500.0, set, "not_one_of", nil)) {
		t.Errorf("500 should be not_one_of the set")
	}
}

func TestCompare_OneOfRequiresList(t *testing.T) {
	res := cmp(t, 200.0, 200.0, "one_of", nil)
	if res.Status != core.StatusError {
		t.Fatalf("status=%q, want error when B isn't a list", res.Status)
	}
}

func TestCompare_InRange2xx(t *testing.T) {
	bounds := []any{200.0, 299.0}
	for _, c := range []struct {
		a    float64
		want bool
	}{
		{199, false}, {200, true}, {204, true}, {299, true}, {300, false}, {500, false},
	} {
		if got(t, cmp(t, c.a, bounds, "in_range", nil)) != c.want {
			t.Errorf("%v in [200,299]: want %v", c.a, c.want)
		}
	}
}

func TestCompare_InRangeExclusiveBounds(t *testing.T) {
	bounds := []any{200.0, 300.0}
	if got(t, cmp(t, 300.0, bounds, "in_range", map[string]any{"inclusive_max": false})) {
		t.Errorf("300 with inclusive_max=false should be out of [200,300)")
	}
	if !got(t, cmp(t, 299.0, bounds, "in_range", map[string]any{"inclusive_max": false})) {
		t.Errorf("299 should be in [200,300)")
	}
}

func TestCompare_NotInRange(t *testing.T) {
	if !got(t, cmp(t, 500.0, []any{200.0, 299.0}, "not_in_range", nil)) {
		t.Errorf("500 should be not_in_range [200,299]")
	}
}

func TestCompare_InRangeBadBounds(t *testing.T) {
	if cmp(t, 200.0, 200.0, "in_range", nil).Status != core.StatusError {
		t.Error("non-list bounds should error")
	}
	if cmp(t, 250.0, []any{299.0, 200.0}, "in_range", nil).Status != core.StatusError {
		t.Error("min > max should error")
	}
}

func TestCompare_ContainsAndNotContains(t *testing.T) {
	if !got(t, cmp(t, "error: cannot connect", "cannot", "contains", nil)) {
		t.Errorf("expected contains match")
	}
	if !got(t, cmp(t, "all good", "error", "not_contains", nil)) {
		t.Errorf("expected not_contains match")
	}
}

func TestCompare_ExistsNotExists(t *testing.T) {
	// exists/not_exists are unary — they test A; B is ignored.
	if !got(t, cmp(t, "something", nil, "exists", nil)) {
		t.Errorf("non-nil A should exist")
	}
	if !got(t, cmp(t, nil, nil, "not_exists", nil)) {
		t.Errorf("nil A should be empty")
	}
}

func TestCompare_FieldPathInA(t *testing.T) {
	a := map[string]any{"response": map[string]any{"status": 404.0}}
	res, _ := executeCompare(t.Context(), core.Job{
		Input:  map[string]core.Ref{"A": {Inline: a}, "B": {Inline: 404.0}},
		Params: map[string]any{"op": "equals", "field": "response.status"},
	}, nil)
	if !got(t, res) {
		t.Errorf("expected match extracting response.status from A")
	}
}

func TestCompare_FieldFromJSONString(t *testing.T) {
	res, _ := executeCompare(t.Context(), core.Job{
		Input:  map[string]core.Ref{"A": {Inline: `{"status":200}`}, "B": {Inline: 200.0}},
		Params: map[string]any{"op": "equals", "field": "status"},
	}, nil)
	if !got(t, res) {
		t.Errorf("expected match extracting status from a JSON-string A")
	}
}

// TestCompare_LiteralParams covers the inline-on-node path: operands come from
// the A/B params (typed as strings) rather than wired inputs, and are
// JSON-coerced — "1000" → number, "[200,299]" → list.
func TestCompare_LiteralParams(t *testing.T) {
	// B literal "1000" coerced to a number for a numeric compare against
	// a wired A.
	res, _ := executeCompare(t.Context(), core.Job{
		Input:  map[string]core.Ref{"A": {Inline: 1500.0}},
		Params: map[string]any{"op": "greater_than", "B": "1000"},
	}, nil)
	if !got(t, res) {
		t.Errorf("1500 > literal \"1000\" should be true")
	}
	// B literal "[200,299]" coerced to a list for in_range.
	res, _ = executeCompare(t.Context(), core.Job{
		Input:  map[string]core.Ref{"A": {Inline: 204.0}},
		Params: map[string]any{"op": "in_range", "B": "[200,299]"},
	}, nil)
	if !got(t, res) {
		t.Errorf("204 in literal \"[200,299]\" should be true")
	}
	// Both operands as literal params, no wires.
	res, _ = executeCompare(t.Context(), core.Job{
		Params: map[string]any{"op": "equals", "A": "hello", "B": "hello"},
	}, nil)
	if !got(t, res) {
		t.Errorf("literal A == literal B should be true")
	}
}

func TestCompare_BadOpErrors(t *testing.T) {
	if cmp(t, "x", "y", "nonsense_op", nil).Status != core.StatusError {
		t.Error("unknown op should error")
	}
}

func TestCompare_OpDefaultsToEquals(t *testing.T) {
	// No op param → defaults to "equals" (the schema default), so a
	// freshly-dropped Compare is immediately valid.
	res, _ := executeCompare(t.Context(), core.Job{
		Input: map[string]core.Ref{"A": {Inline: "x"}, "B": {Inline: "x"}},
	}, nil)
	if !got(t, res) {
		t.Error("missing op should default to equals; x == x is 1")
	}
}
