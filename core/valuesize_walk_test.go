// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// Named container types whose underlying kind is the same as a fast-path
// shape, so a type switch on the concrete type misses them and they exercise
// the reflect arms instead.
type (
	anyList  []any
	anyMap   map[string]any
	oneField struct{ Child any }
	twoField struct{ A, B any }
)

// ApproxGraphBytes sums every caller-supplied string on the graph. Asserting
// only "at least the payload" leaves the sum itself unpinned — a walk that
// subtracts a field, or charges one twice, still trips the ceiling, just at
// the wrong graph size. Every field here has a distinct length, so any single
// wrong sign changes the total.
func TestApproxGraphBytes_ExactCharge(t *testing.T) {
	g := Graph{
		Name: "N", Description: "DD", Icon: "III", // 1 + 2 + 3
		Language: "LLLL", Owner: "OOOOO", // 4 + 5
		Version: "VVVVVV", Visibility: "WWWWWWW", // 6 + 7
		FailureNotify: &FailureNotify{Webhook: "ab", Email: "cde"}, // 2 + 3
		Nodes: []Node{{
			ID: "i", Module: "mm", Label: "lll", // 1 + 2 + 3
			Env:    map[string]string{"e": "ff"}, // 1 + 2
			Params: map[string]any{"p": "qq"},    // 1 + 2
		}},
		Edges: []Edge{{
			From: "a", FromPort: "bb", To: "ccc", ToPort: "dddd", // 1 + 2 + 3 + 4
		}},
		Frames: []Frame{{ID: "f", Title: "tt", Color: "ccc"}}, // 1 + 2 + 3
		Triggers: []GraphTrigger{{
			Type: "t", Cron: "cc", TZ: "zzz", // 1 + 2 + 3
			Secret: "ssss", FormTitle: "fffff", // 4 + 5
			FormFields: []string{"g", "hh"}, // 1 + 2
		}},
	}
	const want = 28 + 5 + 6 + 3 + 3 + 10 + 6 + 15 + 3
	if got := ApproxGraphBytes(g, MaxGraphBytes); got != want {
		t.Errorf("ApproxGraphBytes = %d, want %d", got, want)
	}
}

// The ceiling is inclusive: a value of exactly MaxValueBytes is allowed, and
// only one byte more is refused.
func TestRefTooLarge_LimitIsInclusive(t *testing.T) {
	restore := SetMaxValueBytes(16)
	defer restore()

	if size, too := RefTooLarge(Ref{Inline: strings.Repeat("x", 16)}); too {
		t.Errorf("a value of exactly the 16-byte ceiling was refused (size %d)", size)
	}
	if size, too := RefTooLarge(Ref{Inline: strings.Repeat("x", 17)}); !too {
		t.Errorf("a 17-byte value passed a 16-byte ceiling (size %d)", size)
	}
}

// nest wraps "leaf" n times using wrap, building a chain that descends
// through one arm of the walk.
func nest(n int, wrap func(any) any) any {
	var v any = "leaf"
	for range n {
		v = wrap(v)
	}

	return v
}

// The depth cap has to be reached through EVERY container arm, not just the
// plain []any fast path. Each arm advances the depth on its recursive call;
// an arm that fails to keeps the cap out of reach, and the walk that the cap
// exists to bound — the one that would otherwise hit the stack limit — runs
// all the way to the leaf instead.
func TestApproxValueSize_DepthCapReachedThroughEveryArm(t *testing.T) {
	const budget = 100_000
	deep := maxValueDepth + 5

	for name, wrap := range map[string]func(any) any{
		"map":          func(v any) any { return map[string]any{"k": v} },
		"rowList":      func(v any) any { return []map[string]any{{"k": v}} },
		"refList":      func(v any) any { return []Ref{{Inline: v}} },
		"reflectSlice": func(v any) any { return anyList{v} },
		"reflectMap":   func(v any) any { return anyMap{"k": v} },
		"pointer":      func(v any) any { p := v; return &p },
		"struct":       func(v any) any { return oneField{Child: v} },
	} {
		if got := ApproxValueSize(nest(deep, wrap), budget); got <= budget {
			t.Errorf("%s: a %d-deep value measured %d, want it refused as over the %d budget",
				name, deep, got, budget)
		}
	}
}

// Each arm hands its recursive call the budget it has LEFT, not the budget it
// started with. An arm that passes the full budget down (or adds to it) makes
// the inner walk run on long after the outer total is already spent, which is
// exactly the cost blow-up the early exit exists to prevent.
//
// Every case charges 5 of a 6-byte budget before recursing, so the inner walk
// has 1 byte and must stop after its first 5-byte element: 5 + 5 = 10.
func TestApproxValueSize_EveryArmPassesRemainingBudget(t *testing.T) {
	big := fiveByteList(10)

	for name, c := range map[string]struct {
		v    any
		want int
	}{
		"refList": {[]Ref{{MIME: "aaaaa"}, {MIME: "b", Inline: big}}, 11},
		"rowList": {[]map[string]any{{"a": "aaaa"}, {"b": big}}, 11},
		// A typed slice and a struct both reach the reflect arms.
		"reflectSlice": {anyList{"aaaaa", big}, 10},
		"struct":       {twoField{A: "aaaaa", B: big}, 10},
	} {
		if got := ApproxValueSize(c.v, 6); got != c.want {
			t.Errorf("%s: ApproxValueSize(budget 6) = %d, want %d", name, got, c.want)
		}
	}
}

// A map entry's VALUE is weighed with the budget left after its key, not the
// budget the map started with. A single entry with a five-byte key is enough
// to pin it, and one entry keeps the assertion independent of Go's randomized
// map iteration order.
func TestApproxValueSize_MapValueGetsBudgetLeftAfterKey(t *testing.T) {
	// The key charges 5 of a 6-byte budget, so the value's walk has 1 byte and
	// must stop after its first five-byte element: 5 + 5 = 10.
	if got := ApproxValueSize(anyMap{"aaaaa": fiveByteList(10)}, 6); got != 10 {
		t.Errorf("ApproxValueSize = %d, want 10 (the value got the remaining 1 byte)", got)
	}
}

// ptrChain returns n nested pointers around "leaf". Pointers are comparable,
// so the chain can sit in a map KEY — the one position whose depth accounting
// nothing else exercises.
func ptrChain(n int) any {
	var v any = "leaf"
	for range n {
		p := v
		v = &p
	}

	return v
}

// A map KEY is walked too, and its recursion advances the depth like any
// other. With maxValueDepth-1 pointers the leaf sits exactly on the cap, so
// losing that one increment is the difference between refusing the value and
// walking it to the bottom.
func TestApproxValueSize_MapKeyWalkAdvancesDepth(t *testing.T) {
	const budget = 100
	m := map[any]string{ptrChain(maxValueDepth - 1): "v"}
	if got := ApproxValueSize(m, budget); got <= budget {
		t.Errorf("a key nested to the depth cap measured %d, want it refused as over the %d budget",
			got, budget)
	}
}
