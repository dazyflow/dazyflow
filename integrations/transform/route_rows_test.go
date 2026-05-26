package transform

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// runRoute mirrors the helper shape used by sibling tests in this
// package — pass params + rows, get back a Result.
func runRoute(t *testing.T, params map[string]any, rows []map[string]any, headers []string) core.Result {
	t.Helper()
	in := map[string]core.Ref{"rows": {Inline: rows}}
	if headers != nil {
		in["headers"] = core.Ref{Inline: headers}
	}
	res, err := executeRouteRows(t.Context(), core.Job{ID: "j", Params: params, Input: in}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func slotRows(t *testing.T, res core.Result, slot string) []map[string]any {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q err=%+v", res.Status, res.Error)
	}
	ref, ok := res.Output[slot]
	if !ok {
		t.Fatalf("slot %q not in output (keys=%v)", slot, mapKeys(res.Output))
	}
	rows, ok := ref.Inline.([]map[string]any)
	if !ok {
		t.Fatalf("slot %q inline not []map: %T", slot, ref.Inline)
	}
	return rows
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- Happy paths --------------------------------------------------

func TestRouteRows_ThreeWaySplit(t *testing.T) {
	rows := []map[string]any{
		{"country": "SE", "amount": 100},
		{"country": "NO", "amount": 50},
		{"country": "SE", "amount": 200},
		{"country": "DK", "amount": 10},
	}
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.country == 'SE'"},
				map[string]any{"slot": "rows_2", "filter": "row.country == 'NO'"},
			},
		},
		rows, nil,
	)
	if got := slotRows(t, res, "rows_1"); len(got) != 2 {
		t.Errorf("rows_1 (SE): want 2, got %d", len(got))
	}
	if got := slotRows(t, res, "rows_2"); len(got) != 1 {
		t.Errorf("rows_2 (NO): want 1, got %d", len(got))
	}
	if got := slotRows(t, res, "default"); len(got) != 1 {
		t.Errorf("default (DK): want 1, got %d", len(got))
	}
}

func TestRouteRows_FirstMatchWins(t *testing.T) {
	// Both filters would match — the row should go to rows_1, not
	// be duplicated.
	rows := []map[string]any{{"x": 5}}
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.x > 0"},
				map[string]any{"slot": "rows_2", "filter": "row.x > 0"},
			},
		},
		rows, nil,
	)
	if got := slotRows(t, res, "rows_1"); len(got) != 1 {
		t.Errorf("rows_1: want 1, got %d", len(got))
	}
	if got := slotRows(t, res, "rows_2"); len(got) != 0 {
		t.Errorf("rows_2 (second match should NOT fire): want 0, got %d", len(got))
	}
}

func TestRouteRows_CustomDefaultSlot(t *testing.T) {
	// Use rows_3 as the catch-all instead of "default" — graphs
	// that want a specific port for unmatched can wire it
	// downstream without depending on the default name.
	rows := []map[string]any{
		{"v": "a"}, {"v": "b"},
	}
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.v == 'a'"},
			},
			"default_slot": "rows_3",
		},
		rows, nil,
	)
	if got := slotRows(t, res, "rows_1"); len(got) != 1 {
		t.Errorf("rows_1: want 1, got %d", len(got))
	}
	if got := slotRows(t, res, "rows_3"); len(got) != 1 {
		t.Errorf("rows_3 (custom default): want 1, got %d", len(got))
	}
	// When default_slot is overridden, the "default" port stays
	// dormant (no entry in Output) — consistent with the dormant-
	// slots contract. Asserting absence rather than empty-presence
	// because the engine treats a missing port as "no signal" which
	// keeps downstream branches off the unused name.
	if _, ok := res.Output["default"]; ok {
		t.Errorf("default port should be absent when default_slot is overridden")
	}
}

func TestRouteRows_EmptyRowsAllSlotsEmpty(t *testing.T) {
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.x == 1"},
			},
		},
		nil, []string{"x"},
	)
	if got := slotRows(t, res, "rows_1"); len(got) != 0 {
		t.Errorf("rows_1: want 0, got %d", len(got))
	}
	if got := slotRows(t, res, "default"); len(got) != 0 {
		t.Errorf("default: want 0, got %d", len(got))
	}
}

func TestRouteRows_HeadersPassThrough(t *testing.T) {
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.id > 0"},
			},
		},
		[]map[string]any{{"id": 1, "name": "x"}},
		[]string{"id", "name"},
	)
	headers, ok := res.Output["headers"].Inline.([]string)
	if !ok {
		t.Fatalf("headers not []string: %T", res.Output["headers"].Inline)
	}
	if len(headers) != 2 || headers[0] != "id" || headers[1] != "name" {
		t.Errorf("headers=%v want [id name]", headers)
	}
}

// ---- Error paths --------------------------------------------------

func TestRouteRows_MissingRoutesParamFails(t *testing.T) {
	res := runRoute(t,
		map[string]any{}, // no routes
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestRouteRows_EmptyRoutesFails(t *testing.T) {
	res := runRoute(t,
		map[string]any{"routes": []any{}},
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestRouteRows_UnknownSlotFails(t *testing.T) {
	// "outbox" isn't one of rows_1..rows_8 — must be rejected.
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "outbox", "filter": "row.x > 0"},
			},
		},
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestRouteRows_SlotCollidesWithDefaultFails(t *testing.T) {
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "true"},
			},
			"default_slot": "rows_1", // collision
		},
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestRouteRows_MissingFilterFails(t *testing.T) {
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1"}, // no filter
			},
		},
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param, got %+v", res)
	}
}

func TestRouteRows_BadFilterExpressionFails(t *testing.T) {
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "this is not CEL"},
			},
		},
		[]map[string]any{{"x": 1}}, nil,
	)
	if res.Status != core.StatusError || res.Error.Code != "bad_param" {
		t.Errorf("expected bad_param on compile error, got %+v", res)
	}
}

func TestRouteRows_NonBoolFilterFails(t *testing.T) {
	// CEL expression compiles but returns a string instead of bool.
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "row.x + 'foo'"},
			},
		},
		[]map[string]any{{"x": "a"}}, nil,
	)
	if res.Status != core.StatusError {
		t.Fatalf("expected error on non-bool filter")
	}
	// Either bad_param at compile or eval at runtime depending on
	// how CEL types the expression — both are correct rejections.
	if res.Error.Code != "bad_param" && res.Error.Code != "eval" {
		t.Errorf("expected bad_param or eval, got %q", res.Error.Code)
	}
}

// ---- Slot allocation contract --------------------------------------

func TestRouteRows_DormantSlotsEmitNothing(t *testing.T) {
	// Only rows_1 has a route — rows_2..rows_8 should not appear
	// in the output at all (dormant edges, engine handles
	// downstream skip).
	res := runRoute(t,
		map[string]any{
			"routes": []any{
				map[string]any{"slot": "rows_1", "filter": "true"},
			},
		},
		[]map[string]any{{"x": 1}}, nil,
	)
	for i := 2; i <= 8; i++ {
		slot := "rows_" + string(rune('0'+i))
		if _, ok := res.Output[slot]; ok {
			t.Errorf("dormant slot %q emitted output", slot)
		}
	}
}
