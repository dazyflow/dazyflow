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

func graphJSONBytes(g core.Graph) int {
	b, err := json.Marshal(g)
	if err != nil {
		return -1
	}
	return len(b)
}

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
