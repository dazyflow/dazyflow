package flow

import (
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// runSwitch fetches the switch drop from the registry and runs it with the
// given payload on `in` and the given params, returning the Result. Going
// through the registry proves the drop is registered and routes end to end.
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
		// The WHOLE payload rides out, not just the matched field.
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
	// A list 'equals' matches if the key is any element (one_of semantics).
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

// TestSwitch_WholeValueNoField: with no field param the whole input is the key.
func TestSwitch_WholeValueNoField(t *testing.T) {
	cases := []any{map[string]any{"slot": "case_1", "equals": "vip"}}
	res := runSwitch(t, "vip", map[string]any{"cases": cases})
	if port := routedPort(t, res); port != "case_1" {
		t.Errorf("whole-value match routed to %q, want case_1", port)
	}
}

// TestSwitch_NumericLeniency: a typed-in "200" string coerces to a number and
// matches a numeric key, the same leniency Compare's literals get.
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

// TestSwitch_MissingInput: 'in' is required.
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

// TestSwitch_Manifest locks in the routing metadata: flow_control category
// (blue router tint, like Branch) and NoPassthrough (a pass pin would fire on
// every case and defeat the routing).
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
	// case_1..case_8 + default = 9 output ports.
	if len(m.Outputs) != switchSlotCount+1 {
		t.Errorf("got %d outputs, want %d", len(m.Outputs), switchSlotCount+1)
	}
}
