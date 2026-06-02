package transform

import (
	"reflect"
	"sort"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// runGroup wraps executeGroupAggregate the way runJoin wraps
// executeJoinRows in the sibling test file — keeps per-test
// boilerplate minimal while letting the intent stay readable.
func runGroup(t *testing.T, params map[string]any, rows []map[string]any, headers []string) core.Result {
	t.Helper()
	in := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		in["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeGroupAggregate(t.Context(), core.Job{ID: "j", Params: params, Input: in}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func groupRows(t *testing.T, res core.Result) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows, ok := res.Output["rows"].Inline.([]map[string]any)
	if !ok {
		t.Fatalf("rows output not []map: %T", res.Output["rows"].Inline)
	}
	return rows
}

// rowBy finds the first output row whose group-by column matches.
func rowBy(rows []map[string]any, col string, value any) map[string]any {
	for _, r := range rows {
		if r[col] == value {
			return r
		}
	}
	return nil
}

// ---- Counts --------------------------------------------------------

func TestGroupAggregate_CountByOneColumn(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "order_id": 1},
		{"country": "SE", "order_id": 2},
		{"country": "NO", "order_id": 3},
	}
	res := runGroup(t,
		map[string]any{
			"by":        []any{"country"},
			"aggregate": map[string]any{"n": map[string]any{"op": "count"}},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	if len(out) != 2 {
		t.Fatalf("expected 2 groups, got %d (%+v)", len(out), out)
	}
	if rowBy(out, "country", "SE")["n"] != 2 {
		t.Errorf("SE n=%v want 2", rowBy(out, "country", "SE")["n"])
	}
	if rowBy(out, "country", "NO")["n"] != 1 {
		t.Errorf("NO n=%v want 1", rowBy(out, "country", "NO")["n"])
	}
}

// ---- Sum / avg / type coercion ------------------------------------

func TestGroupAggregate_SumAvgCoerceStringNumeric(t *testing.T) {
	// Excel-stringy values must sum the same as native floats.
	rows := []map[string]any{
		{"country": "SE", "amount": "10"},
		{"country": "SE", "amount": "20.5"},
		{"country": "NO", "amount": 7},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"total":   map[string]any{"op": "sum", "column": "amount"},
				"per_row": map[string]any{"op": "avg", "column": "amount"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	se := rowBy(out, "country", "SE")
	if se["total"] != 30.5 {
		t.Errorf("SE total=%v want 30.5", se["total"])
	}
	if se["per_row"] != 15.25 {
		t.Errorf("SE avg=%v want 15.25", se["per_row"])
	}
	no := rowBy(out, "country", "NO")
	// Integral float sums down-cast to int for tidy display.
	if no["total"] != int64(7) {
		t.Errorf("NO total=%v (%T) want int64(7)", no["total"], no["total"])
	}
}

func TestGroupAggregate_SumOnNonNumericFails(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "name": "alice"},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"s": map[string]any{"op": "sum", "column": "name"},
			},
		},
		rows, nil,
	)
	if res.Status != core.StatusError {
		t.Fatalf("expected error on summing string column")
	}
	if res.Error.Code != "eval" {
		t.Errorf("code=%q", res.Error.Code)
	}
}

func TestGroupAggregate_AvgOfEmptyGroupIsNil(t *testing.T) {
	// Group has rows but the aggregated column is all nil → avg
	// returns nil rather than 0/0=NaN.
	rows := []map[string]any{
		{"country": "SE", "amount": nil},
		{"country": "SE", "amount": nil},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"a": map[string]any{"op": "avg", "column": "amount"},
			},
		},
		rows, []string{"country", "amount"},
	)
	out := groupRows(t, res)
	if out[0]["a"] != nil {
		t.Errorf("avg of all-nil should be nil, got %v", out[0]["a"])
	}
}

// ---- Min / max ----------------------------------------------------

func TestGroupAggregate_MinMaxNumeric(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "score": "10"}, // mixed string-numeric
		{"country": "SE", "score": 30},
		{"country": "SE", "score": 5.5},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"low":  map[string]any{"op": "min", "column": "score"},
				"high": map[string]any{"op": "max", "column": "score"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	if out[0]["low"] != 5.5 {
		t.Errorf("min=%v want 5.5", out[0]["low"])
	}
	if out[0]["high"] != int64(30) {
		t.Errorf("max=%v (%T) want int64(30)", out[0]["high"], out[0]["high"])
	}
}

func TestGroupAggregate_MinMaxLexicalFallback(t *testing.T) {
	// Non-numeric values → lexical comparison.
	rows := []map[string]any{
		{"country": "SE", "name": "carol"},
		{"country": "SE", "name": "alice"},
		{"country": "SE", "name": "bob"},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"first_name_alpha": map[string]any{"op": "min", "column": "name"},
				"last_name_alpha":  map[string]any{"op": "max", "column": "name"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	if out[0]["first_name_alpha"] != "alice" {
		t.Errorf("min=%v want alice", out[0]["first_name_alpha"])
	}
	if out[0]["last_name_alpha"] != "carol" {
		t.Errorf("max=%v want carol", out[0]["last_name_alpha"])
	}
}

// ---- First / last / collect ---------------------------------------

func TestGroupAggregate_FirstLastCollectPreserveInputOrder(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "name": "alice"},
		{"country": "SE", "name": "bob"},
		{"country": "SE", "name": "carol"},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country"},
			"aggregate": map[string]any{
				"first_seen": map[string]any{"op": "first", "column": "name"},
				"last_seen":  map[string]any{"op": "last", "column": "name"},
				"all_names":  map[string]any{"op": "collect", "column": "name"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	r := out[0]
	if r["first_seen"] != "alice" {
		t.Errorf("first=%v want alice", r["first_seen"])
	}
	if r["last_seen"] != "carol" {
		t.Errorf("last=%v want carol", r["last_seen"])
	}
	want := []any{"alice", "bob", "carol"}
	if !reflect.DeepEqual(r["all_names"], want) {
		t.Errorf("collect=%v want %v", r["all_names"], want)
	}
}

// ---- Multi-column by ----------------------------------------------

func TestGroupAggregate_MultiColumnBy(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "tier": "gold", "amount": 100},
		{"country": "SE", "tier": "silver", "amount": 50},
		{"country": "SE", "tier": "gold", "amount": 200},
		{"country": "NO", "tier": "gold", "amount": 75},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{"country", "tier"},
			"aggregate": map[string]any{
				"total": map[string]any{"op": "sum", "column": "amount"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	if len(out) != 3 {
		t.Fatalf("expected 3 groups (SE/gold, SE/silver, NO/gold); got %d (%+v)", len(out), out)
	}
	// (SE, gold) totals 300.
	for _, r := range out {
		if r["country"] == "SE" && r["tier"] == "gold" && r["total"] != int64(300) {
			t.Errorf("SE/gold total=%v want 300", r["total"])
		}
	}
}

// ---- Empty by = total ---------------------------------------------

func TestGroupAggregate_EmptyByIsSingleTotalGroup(t *testing.T) {
	rows := []map[string]any{
		{"amount": 10},
		{"amount": 20},
		{"amount": 30},
	}
	res := runGroup(t,
		map[string]any{
			"by": []any{},
			"aggregate": map[string]any{
				"n":     map[string]any{"op": "count"},
				"total": map[string]any{"op": "sum", "column": "amount"},
			},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	if len(out) != 1 {
		t.Fatalf("len=%d want 1 (single total group)", len(out))
	}
	if out[0]["n"] != 3 {
		t.Errorf("n=%v want 3", out[0]["n"])
	}
	if out[0]["total"] != int64(60) {
		t.Errorf("total=%v want 60", out[0]["total"])
	}
}

// ---- Empty rows ---------------------------------------------------

func TestGroupAggregate_EmptyRowsEmptyOutput(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"country"},
			"aggregate": map[string]any{"n": map[string]any{"op": "count"}},
		},
		nil, []string{"country"},
	)
	if got := groupRows(t, res); len(got) != 0 {
		t.Errorf("empty rows should give empty output, got %d", len(got))
	}
}

// ---- Group order is first-seen ------------------------------------

func TestGroupAggregate_GroupsEmittedInFirstSeenOrder(t *testing.T) {
	rows := []map[string]any{
		{"k": "c"}, {"k": "a"}, {"k": "c"}, {"k": "b"},
	}
	res := runGroup(t,
		map[string]any{
			"by":        []any{"k"},
			"aggregate": map[string]any{"n": map[string]any{"op": "count"}},
		},
		rows, nil,
	)
	out := groupRows(t, res)
	wantOrder := []string{"c", "a", "b"}
	for i, r := range out {
		if r["k"] != wantOrder[i] {
			t.Errorf("group %d = %v want %v (first-seen order)", i, r["k"], wantOrder[i])
		}
	}
}

// ---- Headers contract ---------------------------------------------

func TestGroupAggregate_HeadersAreByThenAlphaAggregates(t *testing.T) {
	rows := []map[string]any{{"a": 1, "x": 5}}
	res := runGroup(t,
		map[string]any{
			"by": []any{"a"},
			"aggregate": map[string]any{
				"zz": map[string]any{"op": "sum", "column": "x"},
				"mm": map[string]any{"op": "count"},
				"aa": map[string]any{"op": "max", "column": "x"},
			},
		},
		rows, nil,
	)
	h, ok := res.Output["headers"].Inline.([]string)
	if !ok {
		t.Fatalf("headers not []string: %T", res.Output["headers"].Inline)
	}
	want := []string{"a", "aa", "mm", "zz"}
	if !reflect.DeepEqual(h, want) {
		t.Errorf("headers=%v want %v", h, want)
	}
}

// ---- Error paths --------------------------------------------------

func TestGroupAggregate_MissingByParamFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{"aggregate": map[string]any{"n": map[string]any{"op": "count"}}},
		[]map[string]any{{"k": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestGroupAggregate_MissingAggregateParamFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{"by": []any{"k"}},
		[]map[string]any{{"k": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestGroupAggregate_EmptyAggregateFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{"by": []any{"k"}, "aggregate": map[string]any{}},
		[]map[string]any{{"k": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestGroupAggregate_BadOpFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"k"},
			"aggregate": map[string]any{"x": map[string]any{"op": "median"}},
		},
		[]map[string]any{{"k": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestGroupAggregate_MissingColumnOnNonCountOpFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"k"},
			"aggregate": map[string]any{"s": map[string]any{"op": "sum"}}, // no column
		},
		[]map[string]any{{"k": 1, "x": 5}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestGroupAggregate_UnknownByColumnFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"missing"},
			"aggregate": map[string]any{"n": map[string]any{"op": "count"}},
		},
		[]map[string]any{{"k": 1}}, []string{"k"},
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("expected bad_input, got %+v", res)
	}
}

func TestGroupAggregate_UnknownAggregateColumnFails(t *testing.T) {
	res := runGroup(t,
		map[string]any{
			"by":        []any{"k"},
			"aggregate": map[string]any{"s": map[string]any{"op": "sum", "column": "missing"}},
		},
		[]map[string]any{{"k": 1, "x": 5}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_input" {
		t.Errorf("expected bad_input, got %+v", res)
	}
}

// ---- Stable headers sort ------------------------------------------

func TestGroupAggregate_HeadersSortedRegardlessOfMapOrder(t *testing.T) {
	// Map iteration in Go is intentionally randomized — run the same
	// input twice and assert the header order is identical so we
	// can pin contract behaviour without flakes.
	build := func() []string {
		res := runGroup(t,
			map[string]any{
				"by": []any{"k"},
				"aggregate": map[string]any{
					"zzz": map[string]any{"op": "count"},
					"aaa": map[string]any{"op": "count"},
					"mmm": map[string]any{"op": "count"},
				},
			},
			[]map[string]any{{"k": 1}}, nil,
		)
		h, _ := res.Output["headers"].Inline.([]string)
		return h
	}
	h1 := build()
	for i := 0; i < 5; i++ {
		h2 := build()
		if !reflect.DeepEqual(h1, h2) {
			t.Errorf("header order not stable across runs: %v vs %v", h1, h2)
		}
	}
	// And alphabetized after the by-cols.
	tail := h1[1:]
	tailCopy := append([]string(nil), tail...)
	sort.Strings(tailCopy)
	if !reflect.DeepEqual(tail, tailCopy) {
		t.Errorf("aggregation header tail not sorted: %v", tail)
	}
}
