// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// The filtering/computing drops expose `now` so flows can express
// time-windows ("overdue", "last week") without precomputing a date
// column. These guard that contract; without `now` in the CEL env the
// expressions fail to compile (the bug the connected journey caught).

func TestComputeRows_NowIsAvailable(t *testing.T) {
	res, err := executeComputeRows(t.Context(), core.Job{
		ID: "t",
		Params: map[string]any{
			"compute": map[string]any{
				"days_old": `(now - timestamp(string(row.d) + "T00:00:00Z")).getHours() / 24`,
			},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{map[string]any{"d": "2020-01-01"}}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v err=%+v", res.Status, res.Error)
	}
	rows := res.Output["rows"].Inline.([]map[string]any)
	days, ok := toInt64(rows[0]["days_old"])
	if !ok || days < 1000 {
		t.Fatalf("days_old = %v, want a large positive day count", rows[0]["days_old"])
	}
}

func TestRouteRows_NowInFilter(t *testing.T) {
	res, err := executeRouteRows(t.Context(), core.Job{
		ID: "t",
		Params: map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": `now > timestamp(string(row.due) + "T00:00:00Z")`},
			},
		},
		Input: map[string]core.Ref{
			"rows": {Inline: []any{
				map[string]any{"id": "past", "due": "2020-01-01"},
				map[string]any{"id": "future", "due": "2999-01-01"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status != core.StatusOK {
		t.Fatalf("status=%v err=%+v", res.Status, res.Error)
	}
	past := res.Output["rows_1"].Inline.([]map[string]any)
	if len(past) != 1 || past[0]["id"] != "past" {
		t.Fatalf("rows_1 = %+v, want only the past-due row", past)
	}
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	}
	return 0, false
}
