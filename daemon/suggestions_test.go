// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon_test

import (
	"context"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/daemon"
)

// find returns the adjacency entry for the from→to module pair, or nil.
func findAdj(items []daemon.DropAdjacency, from, to string) *daemon.DropAdjacency {
	for i := range items {
		if items[i].From == from && items[i].To == to {
			return &items[i]
		}
	}
	return nil
}

// edge is a tiny helper for building valid graph edges (non-empty ports,
// which core.Validate requires).
func edge(from, to string) core.Edge {
	return core.Edge{From: from, FromPort: "out", To: to, ToPort: "in"}
}

func TestDropSuggestions_CountsAndRanking(t *testing.T) {
	h := newVisibilityHarness(t)
	ctx := context.Background()

	// flow1 (org-visible): http_fetch → parse_json AND http_fetch → shell,
	// plus a SECOND http_fetch → parse_json edge (distinct node pair). The
	// duplicate must bump Edges but not Flows — distinct-flow is the signal.
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "flow1", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "http_fetch"},
			{ID: "b", Module: "parse_json"},
			{ID: "c", Module: "shell"},
			{ID: "a2", Module: "http_fetch"},
			{ID: "b2", Module: "parse_json"},
		},
		Edges: []core.Edge{edge("a", "b"), edge("a", "c"), edge("a2", "b2")},
	}); err != nil {
		t.Fatalf("save flow1: %v", err)
	}

	// flow2 (org-visible): http_fetch → parse_json again, in a separate flow.
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "flow2", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "http_fetch"},
			{ID: "b", Module: "parse_json"},
		},
		Edges: []core.Edge{edge("a", "b")},
	}); err != nil {
		t.Fatalf("save flow2: %v", err)
	}

	// bob's PRIVATE flow: cron_trigger → ntfy. Must not leak into alice's
	// suggestions — this is the whole privacy property of own-history mining.
	if _, err := h.svc.SaveGraph(ctx, h.bob, core.Graph{
		ID: "bob-secret", Tenant: "t", Workspace: "ws",
		Visibility: core.VisibilityPrivate,
		Nodes: []core.Node{
			{ID: "x", Module: "cron_trigger"},
			{ID: "y", Module: "ntfy"},
		},
		Edges: []core.Edge{edge("x", "y")},
	}); err != nil {
		t.Fatalf("save bob-secret: %v", err)
	}

	items, err := h.svc.DropSuggestions(ctx, h.alice, "t", "ws")
	if err != nil {
		t.Fatalf("suggestions: %v", err)
	}

	pj := findAdj(items, "http_fetch", "parse_json")
	if pj == nil {
		t.Fatal("missing http_fetch→parse_json")
	}
	if pj.Flows != 2 {
		t.Errorf("parse_json Flows = %d, want 2 (two distinct flows)", pj.Flows)
	}
	if pj.Edges != 3 {
		t.Errorf("parse_json Edges = %d, want 3 (2 in flow1 + 1 in flow2)", pj.Edges)
	}
	if sh := findAdj(items, "http_fetch", "shell"); sh == nil || sh.Flows != 1 {
		t.Errorf("http_fetch→shell = %v, want Flows=1", sh)
	}

	// Privacy: bob's private pairing is invisible to alice.
	if leak := findAdj(items, "cron_trigger", "ntfy"); leak != nil {
		t.Errorf("alice's suggestions leaked bob's private flow: %+v", leak)
	}

	// Ranking: the 2-flow pairing must outrank the 1-flow one.
	if len(items) < 2 || items[0].Flows < items[1].Flows {
		t.Errorf("not sorted by Flows desc: %+v", items)
	}

	// The tenant admin DOES count the private flow (mirrors ListFlowSummaries).
	adminItems, err := h.svc.DropSuggestions(ctx, h.mallory, "t", "ws")
	if err != nil {
		t.Fatalf("admin suggestions: %v", err)
	}
	if findAdj(adminItems, "cron_trigger", "ntfy") == nil {
		t.Error("admin suggestions should include the private flow's pairing")
	}
}

// findAdjPort is findAdj narrowed to a specific source port — for asserting
// the port-level keying that distinguishes a router's matched/unmatched pins.
func findAdjPort(items []daemon.DropAdjacency, from, fromPort, to string) *daemon.DropAdjacency {
	for i := range items {
		if items[i].From == from && items[i].FromPort == fromPort && items[i].To == to {
			return &items[i]
		}
	}
	return nil
}

func TestDropSuggestions_KeysOnSourcePort(t *testing.T) {
	h := newVisibilityHarness(t)
	ctx := context.Background()

	// A router whose two output pins lead to different drops.
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "router-flow", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "r", Module: "route_rows"},
			{ID: "n", Module: "ntfy"},
			{ID: "s", Module: "shell"},
		},
		Edges: []core.Edge{
			{From: "r", FromPort: "matched", To: "n", ToPort: "in"},
			{From: "r", FromPort: "unmatched", To: "s", ToPort: "in"},
		},
	}); err != nil {
		t.Fatalf("save router-flow: %v", err)
	}

	items, err := h.svc.DropSuggestions(ctx, h.alice, "t", "ws")
	if err != nil {
		t.Fatalf("suggestions: %v", err)
	}
	// The two pins must be distinct adjacency entries, not merged.
	if findAdjPort(items, "route_rows", "matched", "ntfy") == nil {
		t.Error("missing route_rows.matched → ntfy")
	}
	if findAdjPort(items, "route_rows", "unmatched", "shell") == nil {
		t.Error("missing route_rows.unmatched → shell")
	}
	// And the matched pin does NOT point at shell (no cross-contamination).
	if findAdjPort(items, "route_rows", "matched", "shell") != nil {
		t.Error("matched pin wrongly merged with the unmatched target")
	}
}

func TestDropSuggestions_EmptyWorkspace(t *testing.T) {
	h := newVisibilityHarness(t)
	items, err := h.svc.DropSuggestions(context.Background(), h.alice, "t", "ws")
	if err != nil {
		t.Fatalf("suggestions: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("empty workspace should yield no suggestions, got %v", items)
	}
}

// TestDropSuggestions_CacheInvalidatesOnSave pins the memo's invalidation
// contract: the result is cached keyed on the workspace HEAD, so a save (which
// moves HEAD) must transparently surface in the next call rather than serving
// a stale cached answer.
func TestDropSuggestions_CacheInvalidatesOnSave(t *testing.T) {
	h := newVisibilityHarness(t)
	ctx := context.Background()

	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "f1", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "http_fetch"}, {ID: "b", Module: "parse_json"}},
		Edges: []core.Edge{edge("a", "b")},
	}); err != nil {
		t.Fatalf("save f1: %v", err)
	}
	// Prime the cache.
	first, err := h.svc.DropSuggestions(ctx, h.alice, "t", "ws")
	if err != nil {
		t.Fatalf("first suggestions: %v", err)
	}
	if findAdj(first, "http_fetch", "parse_json") == nil {
		t.Fatal("expected http_fetch→parse_json after first save")
	}
	if findAdj(first, "http_fetch", "shell") != nil {
		t.Fatal("did not expect http_fetch→shell yet")
	}

	// A second save moves HEAD; the memo must invalidate, not serve the
	// primed result.
	if _, err := h.svc.SaveGraph(ctx, h.alice, core.Graph{
		ID: "f2", Tenant: "t", Workspace: "ws",
		Nodes: []core.Node{{ID: "a", Module: "http_fetch"}, {ID: "c", Module: "shell"}},
		Edges: []core.Edge{edge("a", "c")},
	}); err != nil {
		t.Fatalf("save f2: %v", err)
	}
	second, err := h.svc.DropSuggestions(ctx, h.alice, "t", "ws")
	if err != nil {
		t.Fatalf("second suggestions: %v", err)
	}
	if findAdj(second, "http_fetch", "shell") == nil {
		t.Error("cache was not invalidated on save: new http_fetch→shell edge missing")
	}
}
