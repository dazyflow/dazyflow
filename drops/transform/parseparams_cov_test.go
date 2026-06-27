package transform

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// ===== coerceRowMap (helpers.go alias) ================================

func TestCov_CoerceRowMap(t *testing.T) {
	m, err := coerceRowMap(map[string]any{"a": 1})
	if err != nil || m["a"] != 1 {
		t.Errorf("map: m=%v err=%v", m, err)
	}
	if _, err := coerceRowMap(42); err == nil {
		t.Error("scalar should not coerce to a row map")
	}
}

// ===== parseRouteParams error branches ================================

func TestCov_ParseRouteParamsErrors(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing routes", map[string]any{}},
		{"routes wrong type", map[string]any{"routes": 5}},
		{"empty routes", map[string]any{"routes": []any{}}},
		{"bad default_slot type", map[string]any{"routes": []any{map[string]any{"slot": "rows_1", "filter": "true"}}, "default_slot": 5}},
		{"unknown default_slot", map[string]any{"routes": []any{map[string]any{"slot": "rows_1", "filter": "true"}}, "default_slot": "nope"}},
		{"route not object", map[string]any{"routes": []any{"x"}}},
		{"route missing slot", map[string]any{"routes": []any{map[string]any{"filter": "true"}}}},
		{"unknown slot", map[string]any{"routes": []any{map[string]any{"slot": "rows_99", "filter": "true"}}}},
		{"missing filter", map[string]any{"routes": []any{map[string]any{"slot": "rows_1"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := parseRouteParams(c.params); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// ===== parseJoinParams error branches ================================

func TestCov_ParseJoinParamsBranches(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing on", map[string]any{}},
		{"on bad type", map[string]any{"on": 5}},
		{"empty on", map[string]any{"on": map[string]any{}}},
		{"kind bad type", map[string]any{"on": map[string]any{"a": "b"}, "kind": 5}},
		{"kind unknown", map[string]any{"on": map[string]any{"a": "b"}, "kind": "cross"}},
		{"right_suffix bad type", map[string]any{"on": map[string]any{"a": "b"}, "right_suffix": 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, _, err := parseJoinParams(c.params); err == nil {
				t.Error("expected error")
			}
		})
	}
	// Happy path with explicit kind + suffix.
	on, kind, suffix, err := parseJoinParams(map[string]any{
		"on": map[string]any{"a": "b"}, "kind": "left", "right_suffix": "_r",
	})
	if err != nil || kind != "left" || suffix != "_r" || on["a"] != "b" {
		t.Errorf("happy: on=%v kind=%q suffix=%q err=%v", on, kind, suffix, err)
	}
}

// ===== parseGroupParams error branches ===============================

func TestCov_ParseGroupParamsBranches(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"missing by", map[string]any{}},
		{"bad by", map[string]any{"by": 5}},
		{"missing aggregate", map[string]any{"by": []any{"g"}}},
		{"aggregate bad type", map[string]any{"by": []any{"g"}, "aggregate": 5}},
		{"aggregate empty", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{}}},
		{"spec not object", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{"x": 5}}},
		{"missing op", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{"x": map[string]any{}}}},
		{"op not string", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{"x": map[string]any{"op": 5}}}},
		{"unknown op", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{"x": map[string]any{"op": "median"}}}},
		{"non-count missing column", map[string]any{"by": []any{"g"}, "aggregate": map[string]any{"x": map[string]any{"op": "sum"}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := parseGroupParams(c.params); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// ===== group finalize empty-group branches ===========================

func TestCov_GroupMinMaxFirstLastEmptyGroup(t *testing.T) {
	// A group whose aggregate column holds only nils: numeric path never
	// validates, hasAny stays false → min/max return nil; first/last on
	// a real (non-empty) group return the value.
	res := runGroup(t,
		map[string]any{
			"by": []any{"g"},
			"aggregate": map[string]any{
				"mn": map[string]any{"op": "min", "column": "n"},
				"mx": map[string]any{"op": "max", "column": "n"},
				"fl": map[string]any{"op": "first", "column": "v"},
				"ls": map[string]any{"op": "last", "column": "v"},
			},
		},
		[]map[string]any{
			{"g": "a", "v": "x", "n": nil},
			{"g": "a", "v": "y", "n": nil},
		},
		[]string{"g", "v", "n"})
	rows := groupRows(t, res)
	r := rowBy(rows, "g", "a")
	if r["mn"] != nil || r["mx"] != nil {
		t.Errorf("min/max over all-nil column should be nil: %+v", r)
	}
	if r["fl"] != "x" || r["ls"] != "y" {
		t.Errorf("first/last = %v / %v", r["fl"], r["ls"])
	}
}

// ===== isFenceLang via stripFence (parse_json) =======================

func TestCov_ParseJSONFenceBackticksInProse(t *testing.T) {
	// Fence info line that's NOT a bare lang tag ("{") is treated as
	// content, not stripped — exercises isFenceLang's false branch.
	in := "```\n{\"a\":1}\n```"
	res := runParseJSON(t, in, nil)
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
}
