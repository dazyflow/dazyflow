package flow

import (
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
	"git.sr.ht/~klahr/hazyflow/engine"
)

// runCombinator fetches an AND/OR drop from the registry and runs it with the
// given booleans wired onto the variadic `in` pin (keyed in[0], in[1], …, the
// way the engine routes variadic edges). Going through the registry proves the
// drop is registered and folds end to end. (got() is from compare_test.go.)
func runCombinator(t *testing.T, id string, inputs ...any) bool {
	t.Helper()
	tr, ok := engine.Default.Get(id)
	if !ok {
		t.Fatalf("combinator drop %q not registered", id)
	}
	in := map[string]core.Ref{}
	for i, v := range inputs {
		in[core.VariadicInputKey("in", i)] = core.Ref{Inline: v}
	}
	res, err := tr.Execute(t.Context(), core.Job{Input: in}, nil)
	if err != nil {
		t.Fatalf("%s execute: %v", id, err)
	}
	return got(t, res)
}

func TestCombinators_Behaviour(t *testing.T) {
	for _, c := range []struct {
		id     string
		inputs []any
		want   bool
	}{
		// AND: true only when every input holds.
		{"and", []any{true, true}, true},
		{"and", []any{true, false}, false},
		{"and", []any{true, true, true}, true},
		{"and", []any{true, true, false}, false},
		{"and", []any{true}, true}, // single input folds to itself
		// OR: true when any input holds.
		{"or", []any{false, false}, false},
		{"or", []any{false, true}, true},
		{"or", []any{false, false, false}, false},
		{"or", []any{false, false, true}, true},
		{"or", []any{false}, false},
		// asBool coercion: combinators accept the same shapes Branch.condition
		// does (raw bool, truthy strings, nonzero numbers).
		{"and", []any{"true", "yes"}, true},
		{"or", []any{"false", 0, 1}, true},
	} {
		if g := runCombinator(t, c.id, c.inputs...); g != c.want {
			t.Errorf("%s(%v) = %v, want %v", c.id, c.inputs, g, c.want)
		}
	}
}

// TestCombinators_EmptyInputs: a combinator with nothing wired is a wiring
// error, not a silent true/false.
func TestCombinators_EmptyInputs(t *testing.T) {
	for _, id := range []string{"and", "or"} {
		tr, _ := engine.Default.Get(id)
		res, err := tr.Execute(t.Context(), core.Job{Input: map[string]core.Ref{}}, nil)
		if err != nil {
			t.Fatalf("%s execute: %v", id, err)
		}
		if res.Status == core.StatusOK {
			t.Errorf("%s with no inputs returned OK, want an error status", id)
		}
	}
}

func TestNot_Behaviour(t *testing.T) {
	tr, ok := engine.Default.Get("not")
	if !ok {
		t.Fatalf("not drop not registered")
	}
	for _, c := range []struct {
		in   any
		want bool
	}{
		{true, false},
		{false, true},
		{"yes", false},
		{0, true},
	} {
		res, err := tr.Execute(t.Context(), core.Job{
			Input: map[string]core.Ref{"in": {Inline: c.in}},
		}, nil)
		if err != nil {
			t.Fatalf("not execute: %v", err)
		}
		if g := got(t, res); g != c.want {
			t.Errorf("not(%v) = %v, want %v", c.in, g, c.want)
		}
	}
}

// TestNot_MissingInput: NOT with nothing wired is a wiring error.
func TestNot_MissingInput(t *testing.T) {
	tr, _ := engine.Default.Get("not")
	res, err := tr.Execute(t.Context(), core.Job{Input: map[string]core.Ref{}}, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Status == core.StatusOK {
		t.Errorf("not with no input returned OK, want an error status")
	}
}

// TestCombinators_Manifest locks in the metadata the canvas relies on: each
// combinator is in the "logic" category and leaves Color unset, so the UI
// tints it from the category palette (Blueprint-style), matching the
// comparison primitives.
func TestCombinators_Manifest(t *testing.T) {
	want := map[string]string{"and": "A AND B", "or": "A OR B", "not": "NOT"}
	mans := engine.Default.Manifests()
	for id, label := range want {
		m, ok := mans[id]
		if !ok {
			t.Errorf("combinator %q not registered", id)
			continue
		}
		if m.Label != label {
			t.Errorf("%s label = %q, want %q", id, m.Label, label)
		}
		if m.Category != "logic" {
			t.Errorf("%s category = %q, want logic", id, m.Category)
		}
		if m.Color != "" {
			t.Errorf("%s sets Color=%q; combinators should leave it empty for the category tint", id, m.Color)
		}
	}
}
