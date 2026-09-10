// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// The defect this closes: an author guarding "don't send an empty report"
// reached for the operator the dropdown called "is empty", got `a == nil`,
// and the report went out with no rows in it. "is set" and "is empty" are
// different questions and both are now askable.
func TestIf_EmptinessIsItsOwnQuestion(t *testing.T) {
	rows := func(n int) []map[string]any {
		out := make([]map[string]any, n)
		for i := range out {
			out[i] = map[string]any{"a": i}
		}
		return out
	}
	for name, tc := range map[string]struct {
		value          any
		isSet, isEmpty bool
	}{
		"nothing":           {nil, false, true},
		"no rows":           {rows(0), true, true},
		"some rows":         {rows(2), true, false},
		"empty list of any": {[]any{}, true, true},
		"empty object":      {map[string]any{}, true, true},
		"an object":         {map[string]any{"a": 1}, true, false},
		"blank text":        {"   ", true, true},
		"empty text":        {"", true, true},
		"some text":         {"hello", true, false},
		"zero":              {float64(0), true, false},
		"false":             {false, true, false},
		"empty bytes":       {[]byte(""), true, true},
	} {
		t.Run(name, func(t *testing.T) {
			for op, want := range map[string]bool{
				"exists":     tc.isSet,
				"not_exists": !tc.isSet,
				"is_empty":   tc.isEmpty,
				"not_empty":  !tc.isEmpty,
			} {
				res, err := executeIf(t.Context(), core.Job{
					ID:     "t",
					Params: map[string]any{"op": op},
					Input:  map[string]core.Ref{"A": {Inline: tc.value}},
				}, nil)
				if err != nil {
					t.Fatalf("%s: %v", op, err)
				}
				if res.Status != core.StatusOK {
					t.Fatalf("%s: %+v", op, res.Error)
				}
				wantPort := "else"
				if want {
					wantPort = "then"
				}
				if _, fired := res.Output[wantPort]; !fired {
					t.Errorf("op=%s took the other path (wanted %s), output=%v", op, wantPort, res.Output)
				}
			}
		})
	}
}

// A number and a boolean are values, not absences — 0 sales is a measurement.
func TestCompare_ZeroIsNotEmpty(t *testing.T) {
	for _, v := range []any{float64(0), 0, int64(0), false} {
		if isEmptyValue(v) {
			t.Errorf("%#v read as empty", v)
		}
	}
}

// The guard that would have caught the original defect: the operator the
// dropdown calls "is empty" has to answer yes for a list with nothing in it.
func TestIf_TheOperatorNamedIsEmptyMeansIt(t *testing.T) {
	res, err := executeIf(t.Context(), core.Job{
		ID:     "t",
		Params: map[string]any{"op": "is_empty"},
		Input:  map[string]core.Ref{"A": {Inline: []map[string]any{}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, fired := res.Output["then"]; !fired {
		t.Errorf("an empty row list did not read as empty: %v", res.Output)
	}
}
