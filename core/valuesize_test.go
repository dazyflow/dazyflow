// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestApproxValueSize_CountsBytesNotElements(t *testing.T) {
	cases := map[string]struct {
		v    any
		want int
	}{
		"string":     {"hello", 5},
		"bytes":      {[]byte("hello"), 5},
		"nil":        {nil, 0},
		"list":       {[]any{"ab", "cd"}, 4},
		"map":        {map[string]any{"k": "vv"}, 3},
		"rows":       {[]map[string]any{{"a": "xx"}, {"a": "yy"}}, 6},
		"typedSlice": {[]string{"a", "bb"}, 3},
	}
	for name, c := range cases {
		if got := ApproxValueSize(c.v, 1<<20); got != c.want {
			t.Errorf("%s: ApproxValueSize = %d, want %d", name, got, c.want)
		}
	}
}

// The walk must cost the BUDGET, not the value — measuring a hostile value is
// otherwise as expensive as processing it.
func TestApproxValueSize_StopsAtBudget(t *testing.T) {
	huge := make([]any, 100_000)
	for i := range huge {
		huge[i] = strings.Repeat("x", 100)
	}
	if got := ApproxValueSize(huge, 1000); got <= 1000 {
		t.Errorf("ApproxValueSize = %d, want > budget so the caller rejects it", got)
	}
}

// Nesting deeper than the walk allows is reported as oversized rather than
// walked — the walk is recursive, and a hostile value must not reach the
// stack limit.
func TestApproxValueSize_DepthCapped(t *testing.T) {
	var deep any = "leaf"
	for i := 0; i < maxValueDepth+10; i++ {
		deep = []any{deep}
	}
	if got := ApproxValueSize(deep, 100); got <= 100 {
		t.Errorf("ApproxValueSize = %d on a %d-deep value, want > budget", got, maxValueDepth+10)
	}
}

func TestRefTooLarge(t *testing.T) {
	restore := SetMaxValueBytes(16)
	defer restore()

	if _, too := RefTooLarge(Ref{Inline: "0123456789"}); too {
		t.Error("a 10-byte value tripped a 16-byte ceiling")
	}
	if size, too := RefTooLarge(Ref{Inline: strings.Repeat("x", 32)}); !too || size != 32 {
		t.Errorf("RefTooLarge = (%d, %v), want (32, true)", size, too)
	}
	// Out-of-line refs carry their bytes in the blob store, not the record.
	if _, too := RefTooLarge(Ref{Ref: "blob://" + strings.Repeat("p", 64)}); too {
		t.Error("an out-of-line ref was measured as inline data")
	}
}

func TestSetMaxValueBytes_Restores(t *testing.T) {
	before := MaxValueBytes()
	restore := SetMaxValueBytes(1)
	if MaxValueBytes() != 1 {
		t.Fatalf("MaxValueBytes = %d, want 1", MaxValueBytes())
	}
	restore()
	if MaxValueBytes() != before {
		t.Errorf("MaxValueBytes = %d after restore, want %d", MaxValueBytes(), before)
	}
}

// The graph-byte walk skipped node IDs and module names as "already bounded by
// the node and connection ceilings" — a bound on the COUNT, not the LENGTH.
// Nothing validates a node ID (ValidGraphID covers the flow id only), so the
// same oversize graph the ceiling exists to refuse came back with the payload
// moved into the identifiers: 100 nodes with 256 KiB names measured 500 bytes.
func TestApproxGraphBytes_WeighsIdentifiers(t *testing.T) {
	const pad = 64 << 10
	big := strings.Repeat("n", pad)

	cases := []struct {
		name string
		g    Graph
	}{
		{"node id", Graph{Nodes: []Node{{ID: big, Module: "text"}}}},
		{"module name", Graph{Nodes: []Node{{ID: "a", Module: big}}}},
		{"port names", Graph{
			Nodes: []Node{{ID: "a", Module: "text"}, {ID: "b", Module: "text"}},
			Edges: []Edge{{From: "a", FromPort: big, To: "b", ToPort: big}},
		}},
		{"language", Graph{Language: big}},
		{"failure webhook", Graph{FailureNotify: &FailureNotify{Webhook: big}}},
		// The two repeated sub-records. Frames and triggers each had some of
		// their strings charged and one missed — a frame's ID (nothing
		// anywhere validates it; the engine ignores frames entirely) and a
		// trigger's Type (the scheduler switches on it and ignores what it
		// doesn't know). Their count ceilings bound how MANY, not how big.
		{"frame id", Graph{Frames: []Frame{{ID: big}}}},
		{"frame title", Graph{Frames: []Frame{{ID: "f", Title: big}}}},
		{"trigger type", Graph{Triggers: []GraphTrigger{{Type: big}}}},
		{"trigger cron", Graph{Triggers: []GraphTrigger{{Type: "cron", Cron: big}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApproxGraphBytes(tc.g, MaxGraphBytes); got < pad {
				t.Errorf("ApproxGraphBytes = %d, want at least the %d bytes the graph carries", got, pad)
			}
		})
	}
}

// The walk still stops at the budget: weighing a hostile graph must cost the
// budget, not the graph.
func TestApproxGraphBytes_IdentifierWalkStopsAtBudget(t *testing.T) {
	g := Graph{}
	for range 1000 {
		g.Nodes = append(g.Nodes, Node{ID: strings.Repeat("n", 1<<10), Module: "text"})
	}
	if got := ApproxGraphBytes(g, 4096); got > 4096+(1<<10) {
		t.Errorf("walk ran past the budget: %d", got)
	}
}

// An approval prompt and a run's error message are read off a RUN RESULT, so
// the graph byte budget never sees them and the value ceiling (64 MiB) was
// their only limit — while the operator's own mailer renders each into two
// bodies and sends it once per recipient.
func TestClipNotificationText(t *testing.T) {
	if got := ClipNotificationText("Approve the refund?"); got != "Approve the refund?" {
		t.Errorf("a real prompt was altered: %q", got)
	}
	at := strings.Repeat("a", MaxNotificationTextBytes)
	if got := ClipNotificationText(at); got != at {
		t.Errorf("a string exactly at the limit was clipped")
	}
	long := ClipNotificationText(strings.Repeat("a", 4<<20))
	if len(long) > MaxNotificationTextBytes+len("…") {
		t.Errorf("clipped to %d bytes, want at most %d", len(long), MaxNotificationTextBytes)
	}
	if !strings.HasSuffix(long, "…") {
		t.Errorf("a clipped string should say it was cut")
	}
	// Cutting mid-rune would render as a replacement character in some clients
	// and break quoted-printable encoding in others.
	multibyte := ClipNotificationText(strings.Repeat("ä", MaxNotificationTextBytes))
	if !utf8.ValidString(multibyte) {
		t.Errorf("clipping a multi-byte string produced invalid UTF-8")
	}
}

// A notification LABEL becomes a mail header, where the limit is much harder
// than in a body: RFC 5321 caps a line at 1000 octets and a server that sees a
// longer one drops the connection — so an over-long flow name meant the mail
// was never delivered at all, not that it was large.
func TestClipNotificationLabel(t *testing.T) {
	if got := ClipNotificationLabel("Refund approvals"); got != "Refund approvals" {
		t.Errorf("a real flow name was altered: %q", got)
	}
	long := ClipNotificationLabel(strings.Repeat("N", 2<<20))
	if len(long) > MaxNotificationLabelBytes+len("…") {
		t.Errorf("clipped to %d bytes, want at most %d", len(long), MaxNotificationLabelBytes)
	}
	// Comfortably inside the RFC 5321 line limit, with room for the header
	// name, the surrounding subject text and any encoding overhead.
	if len(long) > 900 {
		t.Errorf("a clipped subject is %d bytes — too close to the 1000-octet line limit", len(long))
	}
	if !utf8.ValidString(ClipNotificationLabel(strings.Repeat("ä", 1000))) {
		t.Errorf("clipping a multi-byte label produced invalid UTF-8")
	}
}
