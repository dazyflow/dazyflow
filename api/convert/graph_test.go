// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package convert

import (
	"reflect"
	"strings"
	"testing"

	controlpb "github.com/dazyflow/dazyflow/api/gen/control"
	"github.com/dazyflow/dazyflow/core"
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

func TestGraphToPB_MarshalError(t *testing.T) {
	// A channel can't be JSON-marshaled, so node param encoding fails.
	g := core.Graph{Nodes: []core.Node{
		{ID: "bad", Params: map[string]any{"ch": make(chan int)}},
	}}
	if _, err := GraphToPB(g); err == nil || !strings.Contains(err.Error(), `marshal params for "bad"`) {
		t.Fatalf("GraphToPB = %v, want marshal error", err)
	}
}

func TestGraphFromPB_NilGraph(t *testing.T) {
	if _, err := GraphFromPB(nil); err == nil || !strings.Contains(err.Error(), "graph required") {
		t.Fatalf("GraphFromPB(nil) = %v, want graph-required error", err)
	}
}

func TestGraphFromPB_UnmarshalError(t *testing.T) {
	g := &controlpb.Graph{Nodes: []*controlpb.Node{
		{Id: "bad", Params: []byte("{not json")},
	}}
	if _, err := GraphFromPB(g); err == nil || !strings.Contains(err.Error(), `unmarshal params for "bad"`) {
		t.Fatalf("GraphFromPB = %v, want unmarshal error", err)
	}
}
