// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package flow

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

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

func TestOperatorPrimitives_Manifest(t *testing.T) {
	want := map[string]string{
		"eq": "A = B", "neq": "A ≠ B",
		"gt": "A > B", "gte": "A ≥ B",
		"lt": "A < B", "lte": "A ≤ B",
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

func TestInRangePrimitive_Removed(t *testing.T) {
	if _, ok := engine.Default.Get("in_range"); ok {
		t.Error("in_range primitive drop is still registered; it should have been trimmed in favour of Compare's in_range op")
	}
}
