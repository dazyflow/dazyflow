package convert

import (
	"reflect"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// TestGraphPBRoundTrip pins the core.Graph <-> controlpb.Graph conversion
// shared by the gRPC daemon handlers and the dzctl client. Uses only the
// fields the conversion carries, with string params (JSON-stable), so a
// clean round-trip must reproduce the graph exactly. Notably guards the
// poll trigger's IntervalSeconds, which used to be dropped over the wire
// on the daemon side before the conversion was unified here.
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

	pb, err := GraphToPB(orig)
	if err != nil {
		t.Fatalf("GraphToPB: %v", err)
	}
	got, err := GraphFromPB(pb)
	if err != nil {
		t.Fatalf("GraphFromPB: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n orig: %#v\n got:  %#v", orig, got)
	}
	// Explicit regression guard: poll interval must survive the gRPC hop.
	// This previously failed on the daemon's copy, which dropped it.
	if got.Triggers[1].IntervalSeconds != 300 {
		t.Errorf("poll IntervalSeconds = %d after round-trip, want 300", got.Triggers[1].IntervalSeconds)
	}
}

// TestGraphFromPBNil mirrors the daemon SaveGraph guard: a nil graph is an
// error rather than a panic.
func TestGraphFromPBNil(t *testing.T) {
	if _, err := GraphFromPB(nil); err == nil {
		t.Fatal("GraphFromPB(nil) = nil error, want error")
	}
}
