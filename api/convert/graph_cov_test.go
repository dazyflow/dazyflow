// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package convert

import (
	"strings"
	"testing"

	controlpb "git.sr.ht/~klahr/dazyflow/api/gen/control"
	"git.sr.ht/~klahr/dazyflow/core"
)

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
