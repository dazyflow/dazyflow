// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package chaos

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dazyflow/dazyflow/core"
)

// graphJSONBytes is what the graph actually costs to store and to re-parse —
// the number the ceilings exist to bound, as opposed to what ApproxGraphBytes
// measures.
func graphJSONBytes(g core.Graph) int {
	b, err := json.Marshal(g)
	if err != nil {
		return -1
	}
	return len(b)
}

// The graph-byte ceiling weighs params, labels, env, frame titles and the
// flow's description. It deliberately skips node IDs and module names —
// "already bounded by the node and connection ceilings" (core.ApproxGraphBytes).
// Those ceilings bound the COUNT of nodes and wires, not the LENGTH of the
// strings naming them, and every identifier is a free-form string from the
// caller: nothing between the API and the store limits one.
//
// So the same shape TestGraphBytes_AreCapped closed with MaxGraphBytes is
// reachable again by moving the payload from the params into the IDs. The
// flow ID is validated (core.ValidGraphID, 128 bytes, slug charset); a NODE
// id is not.
func TestIdentifierBytes_AreCapped(t *testing.T) {
	const (
		nodes  = 100
		idSize = 256 << 10 // 256 KiB per node ID -> ~25 MiB of identifiers
	)
	pad := strings.Repeat("n", idSize)

	g := graph("idbomb", nil, nil)
	for i := range nodes {
		g.Nodes = append(g.Nodes, textNode(pad+string(rune('a'+i%26))+strings.Repeat("z", i), "x"))
	}
	measured := core.ApproxGraphBytes(g, core.MaxGraphBytes)
	actual := graphJSONBytes(g)
	t.Logf("node-ID bomb: ApproxGraphBytes=%d (ceiling %d), actual JSON=%d bytes",
		measured, core.MaxGraphBytes, actual)

	if err := newHarness(t).publish(t, g); err == nil {
		t.Errorf("FINDING: a %d-byte flow (%d nodes, %d KiB of node IDs each) was stored — "+
			"the size walk measured it as %d bytes because it skips identifiers",
			actual, nodes, idSize>>10, measured)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// The module name is the other identifier the size walk skips, and the run
// path deliberately accepts a module it has no manifest for (a tenant's runner
// and MCP drops live outside the default palette). One node is therefore
// enough: the whole payload rides in Node.Module.
func TestModuleNameBytes_AreCapped(t *testing.T) {
	g := graph("modulebomb", []core.Node{
		{ID: "a", Module: "runner." + strings.Repeat("m", 32<<20)}, // 32 MiB
	}, nil)
	measured := core.ApproxGraphBytes(g, core.MaxGraphBytes)
	actual := graphJSONBytes(g)
	t.Logf("module-name bomb: ApproxGraphBytes=%d (ceiling %d), actual JSON=%d bytes",
		measured, core.MaxGraphBytes, actual)

	if err := newHarness(t).publish(t, g); err == nil {
		t.Errorf("FINDING: a one-node flow carrying a %d-byte module name was stored "+
			"(measured as %d bytes)", actual, measured)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// Port names are the third. A catalogued module's ports are checked against
// its manifest, so they can't be long — but a module outside the catalog gets
// no port rules at all (ValidateRuntime says so explicitly), and the fan-in
// rule that does still apply counts wires without looking at the name. So the
// payload moves onto the wires: 200 edges between two catalog-less steps, each
// naming a distinct 128 KiB port.
func TestPortNameBytes_AreCapped(t *testing.T) {
	const (
		edges    = 200
		portSize = 128 << 10
	)
	pad := strings.Repeat("p", portSize)
	g := graph("portbomb", []core.Node{
		{ID: "a", Module: "runner.src"},
		{ID: "b", Module: "runner.dst"},
	}, nil)
	for i := range edges {
		// Distinct port names, so this is not fan-in into one pin — every
		// wire is a legal single-value connection as far as every rule goes.
		suffix := strings.Repeat("q", i)
		g.Edges = append(g.Edges, core.Edge{
			From: "a", FromPort: pad + suffix, To: "b", ToPort: pad + suffix,
		})
	}
	measured := core.ApproxGraphBytes(g, core.MaxGraphBytes)
	actual := graphJSONBytes(g)
	t.Logf("port-name bomb: ApproxGraphBytes=%d (ceiling %d), actual JSON=%d bytes",
		measured, core.MaxGraphBytes, actual)

	if err := newHarness(t).publish(t, g); err == nil {
		t.Errorf("FINDING: a %d-byte flow (2 nodes, %d wires naming %d KiB ports) was stored "+
			"(measured as %d bytes)", actual, edges, portSize>>10, measured)
	} else {
		t.Logf("refused at the save gate: %v", firstLine(err))
	}
}

// A stored oversized graph is only half the cost: every RUN copies it into a
// job record, and the worker re-parses that on each dispatch pass. This runs
// the node-ID bomb to show what one fire of it persists.
func TestIdentifierBomb_RunRecordCost(t *testing.T) {
	h := newHarness(t)
	pad := strings.Repeat("n", 64<<10)
	g := graph("idruncost", []core.Node{
		textNode(pad+"a", "x"),
		textNode(pad+"b", "y"),
	}, nil)
	status, err := h.submit(g, 30*time.Second)
	if err != nil {
		t.Logf("submit refused: %v", firstLine(err))
		return
	}
	t.Logf("status=%s, stored job-record bytes=%d for a graph of %d bytes",
		status, h.storedBytes(g.ID), graphJSONBytes(g))
	if status == statusHung {
		t.Errorf("FINDING: the run never reached a terminal status")
	}
}
