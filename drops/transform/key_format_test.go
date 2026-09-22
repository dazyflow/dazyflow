// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// A JSON number ≥ 1e6 must key the same as the int or string form of it
// (fmt.Sprint renders float64 1000000 as "1e+06").
func TestJoinRows_LargeFloatKeysMatch(t *testing.T) {
	left := []map[string]any{{"id": float64(1234567), "name": "x"}}
	right := []map[string]any{{"user_id": "1234567", "country": "SE"}}
	rows := outRows(t, runJoin(t, map[string]any{"on": map[string]any{"id": "user_id"}}, left, right, nil, nil))
	if len(rows) != 1 || rows[0]["country"] != "SE" {
		t.Fatalf("rows = %+v, want the float id to match its string form", rows)
	}
}

// Rows missing the key column match nothing — not each other.
func TestJoinRows_MissingKeysDoNotMatch(t *testing.T) {
	left := []map[string]any{{"id": nil, "name": "no-id-left"}}
	right := []map[string]any{{"user_id": nil, "country": "no-id-right"}}
	on := map[string]any{"id": "user_id"}
	if rows := outRows(t, runJoin(t, map[string]any{"on": on}, left, right, nil, nil)); len(rows) != 0 {
		t.Errorf("inner join of two keyless rows = %+v, want none", rows)
	}
	rows := outRows(t, runJoin(t, map[string]any{"on": on, "kind": "outer"}, left, right, nil, nil))
	if len(rows) != 2 {
		t.Errorf("outer join = %+v, want both rows unmatched", rows)
	}
}

func TestLookup_LargeNumberAndNil(t *testing.T) {
	table := map[string]any{"1000000": "million", "": "blank"}
	if got := runLookup(t, map[string]any{"table": table}, float64(1000000)).Output["out"].Inline; got != "million" {
		t.Errorf("out = %v, want million", got)
	}
	res, err := executeLookup(t.Context(), core.Job{ID: "t", Params: map[string]any{"table": table},
		Input: map[string]core.Ref{"in": {Inline: nil}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit := res.Output["out"]; hit {
		t.Errorf("a nil value matched the blank-keyed row: %+v", res.Output)
	}
	if _, ok := res.Output["unmatched"]; !ok {
		t.Errorf("nil value should go out 'unmatched': %+v", res.Output)
	}
}
