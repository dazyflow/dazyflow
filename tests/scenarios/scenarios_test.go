// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package scenarios validates the reference automation scenarios in
// scenarios.md against the real native catalog. Each NN-*.json is a graph
// that implements one scenario; the test asserts every graph composes from
// modules and ports that actually exist (ValidateWithManifests), and that
// every for_each has its `body` pin wired to a loop body. A failure here
// means a scenario references a capability we do not yet support — that is
// the gap to close.
package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
	_ "git.sr.ht/~klahr/dazyflow/drops" // register every native drop
	"git.sr.ht/~klahr/dazyflow/engine"
)

// combinedManifests is the full shipped catalog the app exposes. Every drop —
// including the gmail/slack/sheets/… connectors — is now a native Go drop, so
// the catalog is just the native registry. WithPassthrough is applied to each
// manifest exactly as the engine's resolver does at run time (resolver.go), so
// the universal `pass` pin is part of the validated surface — otherwise a graph
// that legitimately wires through a pass pin (e.g. a trigger sequencing a
// downstream step) would be falsely flagged as referencing a missing port.
func combinedManifests(t *testing.T) map[string]core.Manifest {
	t.Helper()
	out := map[string]core.Manifest{}
	for id, m := range engine.Default.Manifests() {
		out[id] = core.WithPassthrough(m)
	}
	return out
}

func TestScenarioGraphsValidate(t *testing.T) {
	manifests := combinedManifests(t)

	files, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatalf("glob scenario graphs: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no scenario graph JSON files found")
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			var g core.Graph
			if err := json.Unmarshal(data, &g); err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			// Validate the graph as it would actually run — after the same
			// data-model migration the daemon applies on load (e.g. dropping
			// folded-away `headers` edges).
			g = core.MigrateGraph(g)

			if err := core.ValidateWithManifests(g, manifests); err != nil {
				t.Fatalf("graph does not compose against the catalog:\n%v", err)
			}

			// for_each runs a body subgraph wired to its `body` pin.
			// ValidateWithManifests already verified the body nodes' modules
			// and ports; assert each for_each actually has the pin wired (an
			// unwired for_each has nothing to run).
			bodyWired := map[string]bool{}
			for _, e := range g.Edges {
				if e.FromPort == "body" {
					bodyWired[e.From] = true
				}
			}
			for _, n := range g.Nodes {
				if n.Module == "for_each" && !bodyWired[n.ID] {
					t.Errorf("for_each node %q has no wired `body` pin (loop body)", n.ID)
				}
			}
		})
	}
}
