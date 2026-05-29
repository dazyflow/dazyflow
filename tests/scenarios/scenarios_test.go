// Package scenarios validates the reference automation scenarios in
// scenarios.md against the real native catalog. Each NN-*.json is a graph
// that implements one scenario; the test asserts every graph composes from
// modules and ports that actually exist (ValidateWithManifests), and that
// every for_each step_module is a registered module too. A failure here
// means a scenario references a capability we do not yet support — that is
// the gap to close.
package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
	"git.sr.ht/~klahr/hazy-flow/engine"
	_ "git.sr.ht/~klahr/hazy-flow/integrations" // register every native drop
)

func TestScenarioGraphsValidate(t *testing.T) {
	manifests := engine.Default.Manifests()

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

			if err := core.ValidateWithManifests(g, manifests); err != nil {
				t.Fatalf("graph does not compose against the catalog:\n%v", err)
			}

			// for_each references its per-item step by module name, so the
			// edge-level check above can't see it. Assert it resolves to a
			// real module (subgraph is referenced by graph_id, not a drop).
			for _, n := range g.Nodes {
				if n.Module != "for_each" {
					continue
				}
				step, _ := n.Params["step_module"].(string)
				if step == "" {
					t.Errorf("for_each node %q has no step_module", n.ID)
					continue
				}
				if step == "subgraph" {
					continue
				}
				if _, ok := manifests[step]; !ok {
					t.Errorf("for_each node %q references unknown step_module %q", n.ID, step)
				}
			}
		})
	}
}
