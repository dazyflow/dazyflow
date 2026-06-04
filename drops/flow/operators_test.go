package flow

import (
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// runOp fetches a primitive operator drop from the registry by ID and runs it
// with A and B wired, returning the 1/0 Result as a bool. Going through the
// registry proves the drop is actually registered AND that it bakes the right
// operator end to end — not just that compareWith works. (got() is from
// compare_test.go.)
func runOp(t *testing.T, id string, a, b any) bool {
	t.Helper()
	tr, ok := engine.Default.Get(id)
	if !ok {
		t.Fatalf("operator drop %q not registered", id)
	}
	res, err := tr.Execute(t.Context(), core.Job{
		Input: map[string]core.Ref{"A": {Inline: a}, "B": {Inline: b}},
	}, nil)
	if err != nil {
		t.Fatalf("%s execute: %v", id, err)
	}
	return got(t, res)
}

func TestOperatorPrimitives_Behaviour(t *testing.T) {
	for _, c := range []struct {
		id   string
		a, b any
		want bool
	}{
		{"eq", 200, 200, true},
		{"eq", 200, 404, false},
		{"neq", "a", "b", true},
		{"neq", "a", "a", false},
		{"gt", 10001, 10000, true},
		{"gt", 10000, 10000, false},
		{"gte", 10000, 10000, true},
		{"gte", 9999, 10000, false},
		{"lt", 5, 10, true},
		{"lt", 10, 10, false},
		{"lte", 10, 10, true},
		{"lte", 11, 10, false},
	} {
		if g := runOp(t, c.id, c.a, c.b); g != c.want {
			t.Errorf("%s(%v,%v) = %v, want %v", c.id, c.a, c.b, g, c.want)
		}
	}
}

// runInRange fetches the in_range ternary primitive and runs it with
// Value/Min/Max wired and optional inclusive-bound params, returning the 1/0
// Result as a bool. Like runOp it goes through the registry, proving the drop
// is registered and its three-pin executor packs [Min, Max] correctly.
func runInRange(t *testing.T, value, min, max any, params map[string]any) bool {
	t.Helper()
	tr, ok := engine.Default.Get("in_range")
	if !ok {
		t.Fatalf("in_range drop not registered")
	}
	res, err := tr.Execute(t.Context(), core.Job{
		Input: map[string]core.Ref{
			"value": {Inline: value},
			"min":   {Inline: min},
			"max":   {Inline: max},
		},
		Params: params,
	}, nil)
	if err != nil {
		t.Fatalf("in_range execute: %v", err)
	}
	return got(t, res)
}

func TestInRangePrimitive_Behaviour(t *testing.T) {
	for _, c := range []struct {
		name            string
		value, min, max any
		params          map[string]any
		want            bool
	}{
		{name: "inside", value: 250, min: 200, max: 299, want: true},
		{name: "below", value: 199, min: 200, max: 299, want: false},
		{name: "above", value: 300, min: 200, max: 299, want: false},
		{name: "lower bound inclusive by default", value: 200, min: 200, max: 299, want: true},
		{name: "upper bound inclusive by default", value: 299, min: 200, max: 299, want: true},
		{name: "lower bound exclusive", value: 200, min: 200, max: 299, params: map[string]any{"inclusive_min": false}, want: false},
		{name: "upper bound exclusive", value: 299, min: 200, max: 299, params: map[string]any{"inclusive_max": false}, want: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if g := runInRange(t, c.value, c.min, c.max, c.params); g != c.want {
				t.Errorf("in_range(%v in [%v,%v], params=%v) = %v, want %v", c.value, c.min, c.max, c.params, g, c.want)
			}
		})
	}
}

// TestInRangePrimitive_LiteralDefaults checks the unwired path: Min/Max typed
// as literal params (strings, JSON-coerced) resolve just like wired pins.
func TestInRangePrimitive_LiteralDefaults(t *testing.T) {
	tr, _ := engine.Default.Get("in_range")
	res, err := tr.Execute(t.Context(), core.Job{
		Input:  map[string]core.Ref{"value": {Inline: 250}},
		Params: map[string]any{"min": "200", "max": "299"},
	}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !got(t, res) {
		t.Errorf("250 in [200,299] from literal params = false, want true")
	}
}

// TestOperatorPrimitives_Manifest locks in the metadata the canvas relies on:
// each primitive is in the "logic" category and leaves Color unset, so the UI
// tints it from the category palette (Blueprint-style) rather than a baked
// per-node color.
func TestOperatorPrimitives_Manifest(t *testing.T) {
	want := map[string]string{
		"eq": "A = B", "neq": "A ≠ B",
		"gt": "A > B", "gte": "A ≥ B",
		"lt": "A < B", "lte": "A ≤ B",
		"in_range": "In Range",
	}
	mans := engine.Default.Manifests()
	for id, label := range want {
		m, ok := mans[id]
		if !ok {
			t.Errorf("primitive %q not registered", id)
			continue
		}
		if m.Label != label {
			t.Errorf("%s label = %q, want %q", id, m.Label, label)
		}
		if m.Category != "logic" {
			t.Errorf("%s category = %q, want logic", id, m.Category)
		}
		if m.Color != "" {
			t.Errorf("%s sets Color=%q; primitives should leave it empty for the category tint", id, m.Color)
		}
	}
}
