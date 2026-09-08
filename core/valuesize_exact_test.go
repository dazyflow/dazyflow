// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// sizedStruct exercises the reflect struct arm with two exported string
// fields of known length.
type sizedStruct struct {
	A string
	B string
}

// fiveByteList returns n strings of five bytes each, so a walk's early exit
// lands on a predictable count.
func fiveByteList(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = strings.Repeat("x", 5)
	}

	return out
}

// The estimate is a byte count, and each arm of the type switch charges a
// specific number. Asserting "bigger than the budget" leaves the arithmetic
// unpinned: a walk that charges a key twice, or forgets it, still trips a
// ceiling — just at the wrong payload size.
func TestApproxValueSize_ExactChargePerArm(t *testing.T) {
	for name, c := range map[string]struct {
		v    any
		want int
	}{
		"bool":         {true, 1},
		"int":          {7, 8},
		"int64":        {int64(7), 8},
		"uint8":        {uint8(3), 8},
		"float64":      {3.5, 8},
		"emptyList":    {[]any{}, 0},
		"emptyMap":     {map[string]any{}, 0},
		"emptyString":  {"", 0},
		"listOfBools":  {[]any{true, true, true}, 3},
		"mapToInt":     {map[string]any{"k": 1}, 1 + 8},
		"nestedMap":    {map[string]any{"ab": map[string]any{"cd": "ef"}}, 2 + 2 + 2},
		"nestedList":   {[]any{[]any{"ab"}, "cd"}, 2 + 2},
		"typedIntList": {[]int{1, 2}, 8 + 8},
		"intKeyedMap":  {map[int]string{1: "aa"}, 8 + 2},
		"struct":       {sizedStruct{A: "ab", B: "cde"}, 2 + 3},
		"pointer":      {&sizedStruct{A: "ab", B: "cde"}, 2 + 3},
	} {
		if got := ApproxValueSize(c.v, 1<<20); got != c.want {
			t.Errorf("%s: ApproxValueSize = %d, want %d", name, got, c.want)
		}
	}
}

// A Ref is charged for its own strings plus whatever it carries inline —
// MIME, Ref, every header, and the payload. Missing any of them is how a
// payload-carrying Ref once passed a ceiling it was thousands of times over.
func TestApproxValueSize_RefExactCharge(t *testing.T) {
	r := Ref{
		MIME:    "text/plain",        // 10
		Ref:     "abc",               // 3
		Headers: []string{"h", "vv"}, // 1 + 2
		Inline:  "12345",             // 5
	}
	if got, want := ApproxValueSize(r, 1<<20), 10+3+1+2+5; got != want {
		t.Errorf("Ref: ApproxValueSize = %d, want %d", got, want)
	}

	list := []Ref{{MIME: "ab", Inline: "xy"}, {MIME: "cd", Inline: "zw"}}
	if got, want := ApproxValueSize(list, 1<<20), (2+2)+(2+2); got != want {
		t.Errorf("[]Ref: ApproxValueSize = %d, want %d", got, want)
	}
}

// The early exit fires when the running total passes the budget, not when it
// reaches it. A walk that stops on equality under-reports by whatever the
// remaining elements carry — and under-reporting is the direction that lets
// an oversized value through.
func TestApproxValueSize_BudgetIsExclusive(t *testing.T) {
	const five = "aaaaa"
	for name, c := range map[string]struct {
		v      any
		budget int
		want   int
	}{
		// Two equal elements, budget exactly the first one's size.
		"typedStringSlice": {[]string{five, "bbbbb"}, 5, 10},
		"anySlice":         {[]any{five, "bbbbb"}, 5, 10},
		"reflectSlice":     {[]int{1, 2}, 8, 16},
		"struct":           {sizedStruct{A: five, B: "bbbbb"}, 5, 10},
		// Equal-sized entries, so map iteration order cannot change the total.
		"stringMap":  {map[string]any{"aa": "bb", "cc": "dd"}, 4, 8},
		"rowList":    {[]map[string]any{{"aa": "bb"}, {"cc": "dd"}}, 4, 8},
		"reflectMap": {map[int]string{1: "aa", 2: "bb"}, 10, 20},
		"refList":    {[]Ref{{MIME: five}, {MIME: "bbbbb"}}, 5, 10},
		// refSize: at exactly the budget it still charges the inline payload.
		"refInline": {Ref{MIME: "ab", Inline: "xyz"}, 2, 5},
	} {
		if got := ApproxValueSize(c.v, c.budget); got != c.want {
			t.Errorf("%s: ApproxValueSize(budget %d) = %d, want %d", name, c.budget, got, c.want)
		}
	}
}

// A nested walk is handed the REMAINING budget, not the whole one. Handing
// down the full budget (or worse, more) is what makes measuring a hostile
// value cost the value instead of the budget: the inner walk keeps going long
// after the outer one has already been exceeded.
func TestApproxValueSize_NestedWalkGetsRemainingBudget(t *testing.T) {
	// Outer charges 5, leaving 1 of a 6-byte budget: the inner walk must stop
	// after its first 5-byte element (5 > 1), for 5 + 5 = 10.
	if got := ApproxValueSize([]any{"aaaaa", fiveByteList(10)}, 6); got != 10 {
		t.Errorf("nested list: ApproxValueSize = %d, want 10 (inner walk got the remaining 1 byte)", got)
	}

	// Same through a Ref: MIME charges 5, so the inline walk gets 1.
	if got := ApproxValueSize(Ref{MIME: "aaaaa", Inline: fiveByteList(10)}, 6); got != 10 {
		t.Errorf("ref inline: ApproxValueSize = %d, want 10", got)
	}
}

// The depth cap is inclusive: a value AT the cap is refused, not walked one
// level further. The walk is recursive, so the cap is what stops a hostile
// value reaching the stack limit instead of the size check.
func TestApproxValueSize_DepthCapIsInclusive(t *testing.T) {
	nest := func(n int) any {
		var v any = "leaf"
		for range n {
			v = []any{v}
		}

		return v
	}

	// maxValueDepth wrappers put the leaf at depth maxValueDepth exactly.
	if got := ApproxValueSize(nest(maxValueDepth), 100); got <= 100 {
		t.Errorf("a value at the depth cap measured %d, want it refused as over the 100-byte budget", got)
	}
	// One level shallower is ordinary data and is walked to the leaf.
	if got := ApproxValueSize(nest(maxValueDepth-1), 100); got != len("leaf") {
		t.Errorf("a value just inside the depth cap measured %d, want %d", got, len("leaf"))
	}
}

// ApproxGraphBytes stops at the budget in each of its four loops, and each
// stops on passing it rather than reaching it.
func TestApproxGraphBytes_BudgetIsExclusivePerLoop(t *testing.T) {
	const five, other = "aaaaa", "bbbbb"
	for name, g := range map[string]Graph{
		"nodes":    {Nodes: []Node{{ID: five}, {ID: other}}},
		"edges":    {Edges: []Edge{{From: five}, {From: other}}},
		"frames":   {Frames: []Frame{{ID: five}, {ID: other}}},
		"triggers": {Triggers: []GraphTrigger{{Type: five}, {Type: other}}},
	} {
		if got := ApproxGraphBytes(g, 5); got != 10 {
			t.Errorf("%s: ApproxGraphBytes(budget 5) = %d, want 10", name, got)
		}
	}
}

// A node's params are weighed with the budget the walk has left after the
// identifiers, so a graph hiding its payload in params cannot outrun the
// ceiling.
func TestApproxGraphBytes_ParamsGetRemainingBudget(t *testing.T) {
	g := Graph{Nodes: []Node{{ID: "aaaaa", Params: map[string]any{"p": fiveByteList(10)}}}}
	// ID charges 5 of the 6-byte budget; the param key charges 1 and its value
	// is weighed with the remaining 1, stopping after one 5-byte element.
	if got := ApproxGraphBytes(g, 6); got != 11 {
		t.Errorf("ApproxGraphBytes = %d, want 11 (params got the remaining budget)", got)
	}
}

// The env override is read only when the variable is actually set, and only a
// positive integer is honoured — anything else leaves the compiled default in
// force rather than silently disabling the ceiling.
func TestEnvValueBytes(t *testing.T) {
	const key = "DAZYFLOW_TEST_VALUE_BYTES"
	if got := envValueBytes("DAZYFLOW_TEST_UNSET_KEY_XYZ", 99); got != 99 {
		t.Errorf("unset: envValueBytes = %d, want the 99 default", got)
	}
	for _, tc := range []struct {
		set  string
		want int
	}{
		{"4096", 4096},
		{"1", 1},
		{"", 99},     // set but empty
		{"0", 99},    // not positive
		{"-5", 99},   // not positive
		{"abc", 99},  // not a number
		{"12.5", 99}, // not an integer
	} {
		t.Setenv(key, tc.set)
		if got := envValueBytes(key, 99); got != tc.want {
			t.Errorf("%s=%q: envValueBytes = %d, want %d", key, tc.set, got, tc.want)
		}
	}
}
