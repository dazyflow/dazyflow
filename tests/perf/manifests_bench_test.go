// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package perf holds benchmarks for the paths a request or a step waits on.
// They live outside their packages because the realistic input is the full
// drop catalog, which those packages cannot import without a cycle.
package perf

import (
	"fmt"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
)

// BenchmarkManifestsForTenant measures the catalog snapshot every graph
// validation, save and submit takes — see NodeResolver.ManifestsForTenant.
func BenchmarkManifestsForTenant(b *testing.B) {
	r := &engine.NodeResolver{Native: engine.Default}
	b.Logf("native drops: %d", len(engine.Default.Manifests()))
	b.ReportAllocs()
	for b.Loop() {
		if len(r.ManifestsForTenant("t")) == 0 {
			b.Fatal("empty catalog")
		}
	}
}

// realisticGraph is a chain of built-in steps with the wiring a real flow
// has, sized like a mid-size customer flow.
func realisticGraph(nodes int) core.Graph {
	g := core.Graph{ID: "g", Tenant: "t", Workspace: "main"}
	for i := range nodes {
		id := fmt.Sprintf("n%d", i)
		g.Nodes = append(g.Nodes, core.Node{
			ID:     id,
			Module: "http_request",
			Params: map[string]any{"url": fmt.Sprintf("https://example.test/%d", i)},
		})
		if i > 0 {
			g.Edges = append(g.Edges, core.Edge{
				From: fmt.Sprintf("n%d", i-1), FromPort: core.PassPort,
				To: id, ToPort: core.PassPort,
			})
		}
	}
	return g
}

// BenchmarkValidateRuntime measures the gate every save and every submit
// runs the flow through, against the real catalog.
func BenchmarkValidateRuntime(b *testing.B) {
	manifests := (&engine.NodeResolver{Native: engine.Default}).ManifestsForTenant("t")
	g := realisticGraph(20)
	b.ReportAllocs()
	for b.Loop() {
		if err := core.ValidateRuntime(g, manifests); err != nil {
			b.Fatalf("ValidateRuntime: %v", err)
		}
	}
}

// BenchmarkSubmitValidation is the pair as a submit actually pays for it:
// the catalog snapshot and the validation, once per run.
func BenchmarkSubmitValidation(b *testing.B) {
	r := &engine.NodeResolver{Native: engine.Default}
	g := realisticGraph(20)
	b.ReportAllocs()
	for b.Loop() {
		if err := core.ValidateRuntime(g, r.ManifestsForTenant("t")); err != nil {
			b.Fatalf("ValidateRuntime: %v", err)
		}
	}
}
