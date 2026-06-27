// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ===== sort_rows =====================================================

func runSort(t *testing.T, params map[string]any, rows []map[string]any, headers []string) []map[string]any {
	t.Helper()
	input := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		input["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeSortRows(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["rows"].Inline.([]map[string]any)
}

func TestSortRows_AscendingString(t *testing.T) {
	got := runSort(t,
		map[string]any{"by": []any{"name"}},
		[]map[string]any{{"name": "Carol"}, {"name": "Alice"}, {"name": "Bob"}},
		nil)
	if got[0]["name"] != "Alice" || got[1]["name"] != "Bob" || got[2]["name"] != "Carol" {
		t.Errorf("got %+v", got)
	}
}

func TestSortRows_DescendingViaObject(t *testing.T) {
	got := runSort(t,
		map[string]any{"by": []any{map[string]any{"column": "score", "desc": true}}},
		[]map[string]any{
			{"name": "A", "score": int64(50)},
			{"name": "B", "score": int64(90)},
			{"name": "C", "score": int64(70)},
		},
		nil)
	if got[0]["name"] != "B" || got[1]["name"] != "C" || got[2]["name"] != "A" {
		t.Errorf("got %+v", got)
	}
}

func TestSortRows_NumericComparisonOnStrings(t *testing.T) {
	// Critical: "10" must sort AFTER "2" — Excel rows arrive as
	// strings and lex sort would be the obvious bug.
	got := runSort(t,
		map[string]any{"by": []any{"n"}},
		[]map[string]any{{"n": "10"}, {"n": "2"}, {"n": "1"}, {"n": "100"}},
		nil)
	want := []string{"1", "2", "10", "100"}
	for i, w := range want {
		if got[i]["n"] != w {
			t.Errorf("position %d = %v, want %v (got = %+v)", i, got[i]["n"], w, got)
			break
		}
	}
}

func TestSortRows_MultiKeyTieBreak(t *testing.T) {
	// Primary key: country ascending; tie-break: age descending.
	got := runSort(t,
		map[string]any{"by": []any{
			"country",
			map[string]any{"column": "age", "desc": true},
		}},
		[]map[string]any{
			{"name": "SE-young", "country": "SE", "age": int64(20)},
			{"name": "NO-old", "country": "NO", "age": int64(60)},
			{"name": "SE-old", "country": "SE", "age": int64(50)},
		},
		nil)
	if got[0]["name"] != "NO-old" || got[1]["name"] != "SE-old" || got[2]["name"] != "SE-young" {
		t.Errorf("got %+v", got)
	}
}

func TestSortRows_NilsSortFirst(t *testing.T) {
	got := runSort(t,
		map[string]any{"by": []any{"score"}},
		[]map[string]any{
			{"name": "A", "score": int64(50)},
			{"name": "B", "score": nil},
			{"name": "C", "score": int64(10)},
		},
		nil)
	if got[0]["name"] != "B" {
		t.Errorf("nil should sort first, got %+v", got)
	}
}

func TestSortRows_Stability(t *testing.T) {
	// Two rows with identical sort key — input order must be
	// preserved (sort.SliceStable).
	got := runSort(t,
		map[string]any{"by": []any{"group"}},
		[]map[string]any{
			{"id": int64(1), "group": "x"},
			{"id": int64(2), "group": "x"},
			{"id": int64(3), "group": "x"},
		},
		nil)
	for i, r := range got {
		if r["id"] != int64(i+1) {
			t.Errorf("stability broken at %d: %+v", i, got)
			break
		}
	}
}

func TestSortRows_HeadersPassThrough(t *testing.T) {
	res, _ := executeSortRows(t.Context(), core.Job{
		Params: map[string]any{"by": []any{"a"}},
		Input: map[string]core.Ref{
			"rows":    {Inline: []map[string]any{{"a": "2"}, {"a": "1"}}},
			"headers": {Inline: []string{"a", "b", "c"}},
		},
	}, nil)
	got := res.Output["rows"].Headers
	if !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("headers = %v, want passed through", got)
	}
}

func TestSortRows_InputNotMutated(t *testing.T) {
	input := []map[string]any{{"n": "3"}, {"n": "1"}, {"n": "2"}}
	_ = runSort(t, map[string]any{"by": []any{"n"}}, input, nil)
	// Original slice order must survive.
	if input[0]["n"] != "3" || input[2]["n"] != "2" {
		t.Errorf("input mutated: %+v", input)
	}
}

func TestSortRows_MissingBy(t *testing.T) {
	res, _ := executeSortRows(t.Context(), core.Job{
		Params: map[string]any{},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSortRows_EmptyBy(t *testing.T) {
	res, _ := executeSortRows(t.Context(), core.Job{
		Params: map[string]any{"by": []any{}},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestSortRows_EmptyRows(t *testing.T) {
	got := runSort(t, map[string]any{"by": []any{"x"}}, []map[string]any{}, nil)
	if len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// ===== dedupe_rows ===================================================

func runDedupe(t *testing.T, params map[string]any, rows []map[string]any, headers []string) (out []map[string]any, dropped int) {
	t.Helper()
	input := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		input["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeDedupeRows(t.Context(), core.Job{Params: params, Input: input}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	return res.Output["rows"].Inline.([]map[string]any),
		res.Output["dropped"].Inline.(int)
}

func TestDedupeRows_WholeRowKeepFirst(t *testing.T) {
	// No 'by' set → dedupe on full row identity (every column).
	got, dropped := runDedupe(t, nil,
		[]map[string]any{
			{"a": "1", "b": "x"},
			{"a": "2", "b": "y"},
			{"a": "1", "b": "x"}, // duplicate of row 0 → drop
			{"a": "1", "b": "z"}, // different b → keep
		},
		[]string{"a", "b"})
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
	// Surviving rows in original order.
	if got[0]["b"] != "x" || got[1]["b"] != "y" || got[2]["b"] != "z" {
		t.Errorf("got %+v", got)
	}
}

func TestDedupeRows_KeepFirstByExplicitColumns(t *testing.T) {
	got, dropped := runDedupe(t,
		map[string]any{"by": []string{"email"}},
		[]map[string]any{
			{"id": int64(1), "email": "a@x", "name": "Alice"},
			{"id": int64(2), "email": "b@x", "name": "Bob"},
			{"id": int64(3), "email": "a@x", "name": "Updated Alice"}, // dupe email → drop
		},
		nil)
	if len(got) != 2 || dropped != 1 {
		t.Fatalf("got %+v, dropped=%d", got, dropped)
	}
	// First wins, so the "Updated Alice" version is dropped.
	if got[0]["name"] != "Alice" {
		t.Errorf("first-wins broken: %+v", got)
	}
}

func TestDedupeRows_KeepLast(t *testing.T) {
	got, _ := runDedupe(t,
		map[string]any{"by": []string{"email"}, "keep": "last"},
		[]map[string]any{
			{"id": int64(1), "email": "a@x", "name": "Alice"},
			{"id": int64(2), "email": "b@x", "name": "Bob"},
			{"id": int64(3), "email": "a@x", "name": "Updated Alice"},
		},
		nil)
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	// "Updated Alice" survives because it's the LAST occurrence of
	// email=a@x. Surviving rows preserve input order:
	if got[0]["id"] != int64(2) || got[1]["id"] != int64(3) {
		t.Errorf("order broken or wrong survivor: %+v", got)
	}
	if got[1]["name"] != "Updated Alice" {
		t.Errorf("last-wins broken: %+v", got[1])
	}
}

func TestDedupeRows_MultipleByColumns(t *testing.T) {
	// Dedupe on the (tenant, id) composite — same id under different
	// tenants is NOT a dupe.
	got, _ := runDedupe(t,
		map[string]any{"by": []string{"tenant", "id"}},
		[]map[string]any{
			{"tenant": "acme", "id": int64(1)},
			{"tenant": "acme", "id": int64(1)},  // dupe
			{"tenant": "other", "id": int64(1)}, // different tenant, keep
		},
		nil)
	if len(got) != 2 {
		t.Errorf("got %+v, want 2 (acme/1 and other/1)", got)
	}
}

func TestDedupeRows_NullsAreEqual(t *testing.T) {
	// Two rows both nil in the dedupe column → counted as duplicates.
	// Matches the typical "treat empty as empty" expectation.
	got, _ := runDedupe(t,
		map[string]any{"by": []string{"score"}},
		[]map[string]any{
			{"name": "A", "score": nil},
			{"name": "B", "score": nil},
		},
		nil)
	if len(got) != 1 {
		t.Errorf("got %+v, want 1", got)
	}
}

func TestDedupeRows_NoDuplicatesPassThrough(t *testing.T) {
	got, dropped := runDedupe(t, nil,
		[]map[string]any{
			{"a": "1"},
			{"a": "2"},
			{"a": "3"},
		},
		[]string{"a"})
	if len(got) != 3 || dropped != 0 {
		t.Errorf("got %+v dropped=%d, want 3/0", got, dropped)
	}
}

func TestDedupeRows_EmptyRows(t *testing.T) {
	got, dropped := runDedupe(t, nil, []map[string]any{}, []string{"a"})
	if len(got) != 0 || dropped != 0 {
		t.Errorf("got %+v dropped=%d, want empty/0", got, dropped)
	}
}

func TestDedupeRows_InvalidKeep(t *testing.T) {
	res, _ := executeDedupeRows(t.Context(), core.Job{
		Params: map[string]any{"keep": "middle"},
		Input: map[string]core.Ref{
			"rows": {Inline: []map[string]any{{"a": "1"}}},
		},
	}, nil)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("status=%q code=%q, want bad_param", res.Status, res.Error.Code)
	}
}

func TestDedupeRows_DroppedCountOutput(t *testing.T) {
	// Verify the dropped output port specifically — useful as input
	// to a downstream webhook saying "found N duplicates."
	_, dropped := runDedupe(t,
		map[string]any{"by": []string{"id"}},
		[]map[string]any{
			{"id": int64(1)}, {"id": int64(1)}, {"id": int64(1)},
			{"id": int64(2)}, {"id": int64(2)},
		},
		nil)
	if dropped != 3 {
		t.Errorf("dropped = %d, want 3", dropped)
	}
}

func TestDedupeRows_JSONRoundtripShape(t *testing.T) {
	res, _ := executeDedupeRows(t.Context(), core.Job{
		Params: map[string]any{"by": []any{"k"}},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"k": "x", "v": "1"},
				map[string]any{"k": "x", "v": "2"},
				map[string]any{"k": "y", "v": "3"},
			}},
		},
	}, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	if len(rows) != 2 {
		t.Errorf("rows = %d, want 2", len(rows))
	}
}
