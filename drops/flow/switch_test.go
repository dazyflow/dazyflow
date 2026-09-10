// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func runSwitch(t *testing.T, payload any, params map[string]any) core.Result {
	t.Helper()
	tr, ok := engine.Default.Get("switch")
	if !ok {
		t.Fatalf("switch drop not registered")
	}
	res, err := tr.Execute(t.Context(), core.Job{
		Input:  map[string]core.Ref{"in": {Inline: payload}},
		Params: params,
	}, nil)
	if err != nil {
		t.Fatalf("switch execute: %v", err)
	}
	return res
}

// routedPort asserts exactly one output port is set (the routing invariant —
// like Branch, the payload rides out one port) and returns its name.
func routedPort(t *testing.T, res core.Result) string {
	t.Helper()
	if res.Status != core.StatusOK {
		t.Fatalf("status=%q (%+v)", res.Status, res.Error)
	}
	if len(res.Output) != 1 {
		t.Fatalf("expected exactly one output port, got %d: %v", len(res.Output), res.Output)
	}
	for port := range res.Output {
		return port
	}
	return ""
}

func TestSwitch_FieldMatch(t *testing.T) {
	cases := []any{
		map[string]any{"slot": "case_1", "equals": "paid"},
		map[string]any{"slot": "case_2", "equals": "refunded"},
		map[string]any{"slot": "case_3", "equals": "failed"},
	}
	for _, c := range []struct {
		status   string
		wantPort string
	}{
		{"paid", "case_1"},
		{"refunded", "case_2"},
		{"failed", "case_3"},
		{"pending", "default"}, // matches nothing
	} {
		order := map[string]any{"id": 7, "status": c.status}
		res := runSwitch(t, order, map[string]any{"field": "status", "cases": cases})
		if port := routedPort(t, res); port != c.wantPort {
			t.Errorf("status %q routed to %q, want %q", c.status, port, c.wantPort)
		}
		if got := res.Output[c.wantPort].Inline; got == nil {
			t.Errorf("status %q: payload missing on %q", c.status, c.wantPort)
		}
	}
}

func TestSwitch_FirstMatchWins(t *testing.T) {
	// Two cases both match 95; the first listed must win.
	cases := []any{
		map[string]any{"slot": "case_1", "equals": 95},
		map[string]any{"slot": "case_2", "equals": 95},
	}
	res := runSwitch(t, 95, map[string]any{"cases": cases})
	if port := routedPort(t, res); port != "case_1" {
		t.Errorf("first-match-wins: routed to %q, want case_1", port)
	}
}

func TestSwitch_ListEqualsMatchesAny(t *testing.T) {
	cases := []any{
		map[string]any{"slot": "case_1", "equals": []any{200.0, 201.0, 204.0}},
		map[string]any{"slot": "case_2", "equals": []any{400.0, 404.0, 422.0}},
	}
	for _, c := range []struct {
		status   float64
		wantPort string
	}{
		{200, "case_1"},
		{204, "case_1"},
		{404, "case_2"},
		{500, "default"},
	} {
		res := runSwitch(t, c.status, map[string]any{"cases": cases})
		if port := routedPort(t, res); port != c.wantPort {
			t.Errorf("status %v routed to %q, want %q", c.status, port, c.wantPort)
		}
	}
}

func TestSwitch_WholeValueNoField(t *testing.T) {
	cases := []any{map[string]any{"slot": "case_1", "equals": "vip"}}
	res := runSwitch(t, "vip", map[string]any{"cases": cases})
	if port := routedPort(t, res); port != "case_1" {
		t.Errorf("whole-value match routed to %q, want case_1", port)
	}
}

func TestSwitch_NumericLeniency(t *testing.T) {
	cases := []any{map[string]any{"slot": "case_1", "equals": "200"}}
	res := runSwitch(t, 200.0, map[string]any{"cases": cases})
	if port := routedPort(t, res); port != "case_1" {
		t.Errorf("string \"200\" vs numeric key routed to %q, want case_1", port)
	}
}

func TestSwitch_BadParams(t *testing.T) {
	tr, _ := engine.Default.Get("switch")
	for _, c := range []struct {
		name   string
		params map[string]any
	}{
		{"no cases", map[string]any{}},
		{"empty cases", map[string]any{"cases": []any{}}},
		{"unknown slot", map[string]any{"cases": []any{map[string]any{"slot": "case_99", "equals": "x"}}}},
		{"slot collides with default", map[string]any{"cases": []any{map[string]any{"slot": "default", "equals": "x"}}}},
		{"missing equals", map[string]any{"cases": []any{map[string]any{"slot": "case_1"}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := tr.Execute(t.Context(), core.Job{
				Input:  map[string]core.Ref{"in": {Inline: "x"}},
				Params: c.params,
			}, nil)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if res.Status == core.StatusOK {
				t.Errorf("%s returned OK, want an error status", c.name)
			}
		})
	}
}

func TestSwitch_MissingInput(t *testing.T) {
	tr, _ := engine.Default.Get("switch")
	res, err := tr.Execute(t.Context(), core.Job{
		Params: map[string]any{"cases": []any{map[string]any{"slot": "case_1", "equals": "x"}}},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Errorf("missing 'in' returned OK, want an error status")
	}
}

// Locks in the routing metadata: flow_control category (blue router tint, like
// Branch) and NoPassthrough (a pass pin would fire on every case and defeat
// the routing).
func TestSwitch_Manifest(t *testing.T) {
	m, ok := engine.Default.Manifests()["switch"]
	if !ok {
		t.Fatal("switch not registered")
	}
	if m.Category != "flow_control" {
		t.Errorf("category = %q, want flow_control", m.Category)
	}
	if !m.NoPassthrough {
		t.Error("switch must set NoPassthrough — a pass pin defeats routing")
	}
	if len(m.Outputs) != switchSlotCount+1 {
		t.Errorf("got %d outputs, want %d", len(m.Outputs), switchSlotCount+1)
	}
}

// A condition says what equality cannot: a threshold, and two things at once.
func TestSwitch_RoutesOnACondition(t *testing.T) {
	cases := []any{
		map[string]any{"slot": "case_1", "filter": "row.status == 'paid' && row.amount > 100"},
		map[string]any{"slot": "case_2", "filter": "row.status == 'paid'"},
		map[string]any{"slot": "case_3", "filter": "row.status == 'refunded'"},
	}
	for name, tc := range map[string]struct {
		order map[string]any
		want  string
	}{
		"big and paid":    {map[string]any{"status": "paid", "amount": 250.0}, "case_1"},
		"small and paid":  {map[string]any{"status": "paid", "amount": 20.0}, "case_2"},
		"refunded":        {map[string]any{"status": "refunded", "amount": 250.0}, "case_3"},
		"nothing matches": {map[string]any{"status": "failed", "amount": 250.0}, "default"},
	} {
		t.Run(name, func(t *testing.T) {
			got := routedPort(t, runSwitch(t, tc.order, map[string]any{"cases": cases}))
			if got != tc.want {
				t.Errorf("routed to %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole value travels on whichever way it was matched.
func TestSwitch_ConditionCarriesTheWholePayload(t *testing.T) {
	order := map[string]any{"status": "paid", "amount": 250.0, "id": "A-1"}
	res := runSwitch(t, order, map[string]any{
		"cases": []any{map[string]any{"slot": "case_1", "filter": "row.amount > 100"}},
	})
	got, ok := res.Output["case_1"].Inline.(map[string]any)
	if !ok || got["id"] != "A-1" {
		t.Errorf("case_1 carried %v, want the whole order", res.Output["case_1"].Inline)
	}
}

// One switch can hold both kinds of match, and first-match-wins spans them.
func TestSwitch_MixesValuesAndConditions(t *testing.T) {
	params := map[string]any{
		"field": "status",
		"cases": []any{
			map[string]any{"slot": "case_1", "filter": "row.amount > 1000"},
			map[string]any{"slot": "case_2", "equals": "paid"},
		},
	}
	// The condition is first, so a huge paid order takes it...
	if got := routedPort(t, runSwitch(t, map[string]any{"status": "paid", "amount": 5000.0}, params)); got != "case_1" {
		t.Errorf("routed to %q, want case_1", got)
	}
	// ...and a small one falls through to the value match, which reads the
	// field named by 'field' while the condition read the whole value.
	if got := routedPort(t, runSwitch(t, map[string]any{"status": "paid", "amount": 5.0}, params)); got != "case_2" {
		t.Errorf("routed to %q, want case_2", got)
	}
}

// The condition editor can only write row.<field>, so a bare value has to be
// reachable as one.
func TestSwitch_ConditionOnAPlainValue(t *testing.T) {
	params := map[string]any{
		"cases": []any{map[string]any{"slot": "case_1", "filter": "row.value >= 500"}},
	}
	if got := routedPort(t, runSwitch(t, 503.0, params)); got != "case_1" {
		t.Errorf("routed to %q, want case_1", got)
	}
	if got := routedPort(t, runSwitch(t, 200.0, params)); got != "default" {
		t.Errorf("routed to %q, want default", got)
	}
}

func TestSwitch_ConditionFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		params  map[string]any
		payload any
		code    string
		says    string
	}{
		"both kinds on one case": {
			map[string]any{"cases": []any{map[string]any{"slot": "case_1", "equals": "paid", "filter": "row.amount > 1"}}},
			map[string]any{"status": "paid"}, "bad_param", "keep the one you meant",
		},
		"neither kind": {
			map[string]any{"cases": []any{map[string]any{"slot": "case_1"}}},
			map[string]any{"status": "paid"}, "bad_param", "needs a value to look for or a condition",
		},
		"unreadable condition": {
			map[string]any{"cases": []any{map[string]any{"slot": "case_1", "filter": "row.amount >>> 1"}}},
			map[string]any{"amount": 2.0}, "bad_param", "condition",
		},
		"condition that cannot run": {
			map[string]any{"cases": []any{map[string]any{"slot": "case_1", "filter": "row.missing > 1"}}},
			map[string]any{"amount": 2.0}, "eval", "case_1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := runSwitch(t, tc.payload, tc.params)
			if res.Status != core.StatusError || res.Error.Code != tc.code {
				t.Fatalf("status=%q err=%+v, want %s", res.Status, res.Error, tc.code)
			}
			if !strings.Contains(res.Error.Message, tc.says) {
				t.Errorf("message = %q, want it to mention %q", res.Error.Message, tc.says)
			}
		})
	}
}
