package transform

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

func runSplit(t *testing.T, params map[string]any, rows []map[string]any, headers []string) (matched, unmatched []map[string]any, outHeaders []string) {
	t.Helper()
	input := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		input["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeSplitRows(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["matched"].Inline.([]map[string]any),
		res.Output["unmatched"].Inline.([]map[string]any),
		res.Output["matched"].Headers
}

func TestSplitRows_BasicPredicate(t *testing.T) {
	matched, unmatched, _ := runSplit(t,
		map[string]any{"filter": "row.active == true"},
		[]map[string]any{
			{"name": "A", "active": true},
			{"name": "B", "active": false},
			{"name": "C", "active": true},
		},
		nil)
	if len(matched) != 2 || matched[0]["name"] != "A" || matched[1]["name"] != "C" {
		t.Errorf("matched = %+v", matched)
	}
	if len(unmatched) != 1 || unmatched[0]["name"] != "B" {
		t.Errorf("unmatched = %+v", unmatched)
	}
}

func TestSplitRows_AllMatch(t *testing.T) {
	matched, unmatched, _ := runSplit(t,
		map[string]any{"filter": "true"},
		[]map[string]any{{"a": "1"}, {"a": "2"}},
		nil)
	if len(matched) != 2 || len(unmatched) != 0 {
		t.Errorf("matched=%d unmatched=%d, want 2/0", len(matched), len(unmatched))
	}
}

func TestSplitRows_NoneMatch(t *testing.T) {
	matched, unmatched, _ := runSplit(t,
		map[string]any{"filter": "false"},
		[]map[string]any{{"a": "1"}, {"a": "2"}},
		nil)
	if len(matched) != 0 || len(unmatched) != 2 {
		t.Errorf("matched=%d unmatched=%d, want 0/2", len(matched), len(unmatched))
	}
}

func TestSplitRows_ComplexPredicate(t *testing.T) {
	// Multi-column predicate, real ETL shape: keep rows that look
	// "complete" (active + has email + score above threshold).
	matched, unmatched, _ := runSplit(t,
		map[string]any{
			"filter": "row.active == true && size(row.email) > 0 && row.score >= 50",
		},
		[]map[string]any{
			{"id": 1, "active": true, "email": "a@x", "score": int64(80)},  // keep
			{"id": 2, "active": false, "email": "b@x", "score": int64(80)}, // inactive
			{"id": 3, "active": true, "email": "", "score": int64(80)},     // no email
			{"id": 4, "active": true, "email": "d@x", "score": int64(10)},  // low score
		},
		nil)
	if len(matched) != 1 || matched[0]["id"] != 1 {
		t.Errorf("matched = %+v, want only id=1", matched)
	}
	if len(unmatched) != 3 {
		t.Errorf("unmatched = %+v, want 3", unmatched)
	}
}

func TestSplitRows_HeadersPassThroughSharedPort(t *testing.T) {
	_, _, headers := runSplit(t,
		map[string]any{"filter": "true"},
		[]map[string]any{{"a": "1"}},
		[]string{"a", "b", "c"})
	if !reflect.DeepEqual(headers, []string{"a", "b", "c"}) {
		t.Errorf("headers = %v, want passed through", headers)
	}
}

func TestSplitRows_OrderPreservedInEachBranch(t *testing.T) {
	// Critical for "review queue keeps the same order it came in"
	// flows — the relative order of rows within each branch must
	// match input order.
	matched, unmatched, _ := runSplit(t,
		map[string]any{"filter": "row.bucket == 'A'"},
		[]map[string]any{
			{"id": int64(1), "bucket": "A"},
			{"id": int64(2), "bucket": "B"},
			{"id": int64(3), "bucket": "A"},
			{"id": int64(4), "bucket": "B"},
			{"id": int64(5), "bucket": "A"},
		},
		nil)
	for i, want := range []int64{1, 3, 5} {
		if matched[i]["id"] != want {
			t.Errorf("matched[%d].id = %v, want %v", i, matched[i]["id"], want)
		}
	}
	for i, want := range []int64{2, 4} {
		if unmatched[i]["id"] != want {
			t.Errorf("unmatched[%d].id = %v, want %v", i, unmatched[i]["id"], want)
		}
	}
}

func TestSplitRows_InputNotMutated(t *testing.T) {
	// We append rows to one of two slices but don't copy them —
	// confirm we don't accidentally rewrite the underlying maps.
	input := []map[string]any{
		{"a": "1", "active": true},
		{"a": "2", "active": false},
	}
	_, _, _ = runSplit(t, map[string]any{"filter": "row.active"}, input, nil)
	if input[0]["a"] != "1" || input[1]["a"] != "2" {
		t.Errorf("input mutated: %+v", input)
	}
}

func TestSplitRows_EmptyInput(t *testing.T) {
	matched, unmatched, _ := runSplit(t,
		map[string]any{"filter": "true"}, []map[string]any{}, []string{"a"})
	if len(matched) != 0 || len(unmatched) != 0 {
		t.Errorf("got %d/%d, want 0/0", len(matched), len(unmatched))
	}
}

// --- Error paths -----------------------------------------------------

func TestSplitRows_MissingRowsInput(t *testing.T) {
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "true"},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "missing_input" {
		t.Errorf("status=%q code=%q, want missing_input", res.Status, res.Error.Code)
	}
}

func TestSplitRows_MissingFilter(t *testing.T) {
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSplitRows_BadCELSyntax(t *testing.T) {
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "row.a +"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": int64(1)}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSplitRows_FilterMustReturnBool(t *testing.T) {
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "row.a + 1"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": int64(1)}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status=%q code=%q, want eval", res.Status, res.Error.Code)
	}
}

func TestSplitRows_RuntimeErrorFailsBatch(t *testing.T) {
	// Field missing on a row → CEL runtime error → batch fails.
	// Same all-or-nothing contract as compute_rows.
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "row.missing_field == true"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "eval" {
		t.Errorf("status=%q code=%q, want eval", res.Status, res.Error.Code)
	}
}

func TestSplitRows_JSONRoundtripShape(t *testing.T) {
	// gRPC/MCP path: rows arrive as []any of map[string]any.
	res, _ := executeSplitRows(t.Context(), core.Job{
		Params: map[string]any{"filter": "row.n > 5"},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"n": int64(10)},
				map[string]any{"n": int64(3)},
			}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	matched := res.Output["matched"].Inline.([]map[string]any)
	unmatched := res.Output["unmatched"].Inline.([]map[string]any)
	if len(matched) != 1 || matched[0]["n"] != int64(10) {
		t.Errorf("matched = %+v", matched)
	}
	if len(unmatched) != 1 || unmatched[0]["n"] != int64(3) {
		t.Errorf("unmatched = %+v", unmatched)
	}
}
