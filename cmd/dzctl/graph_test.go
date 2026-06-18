package main

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestGraphPBRoundTrip pins the core.Graph ↔ controlpb.Graph conversion
// the gRPC control path (dzctl) relies on. Uses only the fields the
// conversion carries, with string params (JSON-stable), so a clean
// round-trip must reproduce the graph exactly. Notably guards the poll
// trigger's IntervalSeconds, which used to be dropped over the wire.
func TestGraphPBRoundTrip(t *testing.T) {
	orig := core.Graph{
		ID: "g1", Version: "v2", Tenant: "acme", Workspace: "ws",
		Nodes: []core.Node{
			{ID: "a", Module: "file_read", Params: map[string]any{"path": "in.txt"}, Env: map[string]string{"X": "1"}},
			{ID: "b", Module: "file_write", Params: map[string]any{"path": "out.txt"}},
		},
		Edges: []core.Edge{
			{From: "a", FromPort: "out", To: "b", ToPort: "in", OnError: core.OnErrorRetry},
		},
		Triggers: []core.GraphTrigger{
			{Type: "cron", Cron: "0 3 * * *"},
			{Type: "poll", IntervalSeconds: 300},
			{Type: "webhook", Secret: "shh"},
		},
	}

	pb, err := graphToPB(orig)
	if err != nil {
		t.Fatalf("graphToPB: %v", err)
	}
	got, err := graphFromPB(pb)
	if err != nil {
		t.Fatalf("graphFromPB: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n orig: %#v\n got:  %#v", orig, got)
	}
	// Explicit regression guard: poll interval must survive the gRPC hop.
	if got.Triggers[1].IntervalSeconds != 300 {
		t.Errorf("poll IntervalSeconds = %d after round-trip, want 300", got.Triggers[1].IntervalSeconds)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hi", 5); got != "hi" {
		t.Errorf("truncate(short) = %q, want hi", got)
	}
	if got := truncate("hello world", 5); got != "hell…" {
		t.Errorf("truncate(long) = %q, want hell…", got)
	}
	if got := truncate("x", 0); got != "" {
		t.Errorf("truncate(n=0) = %q, want empty", got)
	}
}

func TestRequiredMark(t *testing.T) {
	if requiredMark(true) != " (required)" {
		t.Error("requiredMark(true) wrong")
	}
	if requiredMark(false) != "" {
		t.Error("requiredMark(false) should be empty")
	}
}
