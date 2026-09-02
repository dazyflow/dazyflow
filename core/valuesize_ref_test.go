// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// A Ref must be charged for what it carries. Measured as one word, a list of
// Refs (Merge's out port, for_each's results) let a value thousands of times
// over the ceiling through both guards that enforce it.
func TestApproxValueSize_WalksRefs(t *testing.T) {
	payload := strings.Repeat("x", 4096)

	if got := ApproxValueSize(Ref{MIME: "text/plain", Inline: payload}, 1<<20); got < len(payload) {
		t.Errorf("a Ref carrying %d bytes measured %d", len(payload), got)
	}

	list := make([]Ref, 16)
	for i := range list {
		list[i] = Ref{MIME: "text/plain", Inline: payload}
	}
	want := len(list) * len(payload)
	if got := ApproxValueSize(list, 1<<20); got < want {
		t.Errorf("[]Ref of %d × %d bytes measured %d, want at least %d",
			len(list), len(payload), got, want)
	}

	// Nested, the shape a chain of Merge steps builds.
	nested := any(list)
	for i := 0; i < 3; i++ {
		nested = []Ref{{Inline: nested}, {Inline: nested}}
	}
	if got := ApproxValueSize(nested, 1<<20); got < 8*want {
		t.Errorf("nested []Ref measured %d, want at least %d", got, 8*want)
	}
}

// The walk still stops at the budget rather than measuring a hostile value in
// full, and still terminates on a struct.
func TestApproxValueSize_RefWalkStaysBounded(t *testing.T) {
	big := strings.Repeat("x", 1<<16)
	list := make([]Ref, 4096)
	for i := range list {
		list[i] = Ref{Inline: big}
	}
	const budget = 1024
	got := ApproxValueSize(list, budget)
	if got <= budget {
		t.Errorf("measured %d, want over the %d budget", got, budget)
	}
	if got > budget+len(big) {
		t.Errorf("measured %d — the walk ran past the budget instead of stopping at it", got)
	}
}

// A plain struct is walked too, so a future payload-carrying one isn't a fresh
// hole; unexported fields keep the word charge.
func TestApproxValueSize_WalksExportedStructFields(t *testing.T) {
	type carrier struct {
		Body   string
		hidden string
	}
	body := strings.Repeat("y", 2048)
	got := ApproxValueSize(carrier{Body: body, hidden: body}, 1<<20)
	if got < len(body) {
		t.Errorf("struct carrying %d bytes measured %d", len(body), got)
	}
}
