package scenarios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"git.sr.ht/~klahr/hazyflow/core"
)

// knownBrokenTemplates are shipped templates with a real defect that
// predates this guard, with the finding noted. They're skipped (loudly)
// rather than silently passing, so the gap stays visible until fixed.
var knownBrokenTemplates = map[string]string{
	"gmail-new-email-to-slack.json": "for_each.results is a list of Result wrappers ({status, output:{message}}), but the downstream compute_rows reads row.headers.* as if each item were the message itself. Needs a results->rows flattening step before it can run correctly.",
	"gmail-to-sheet.json":           "same shape mismatch as gmail-new-email-to-slack: for_each.results (wrapped Results) feeds compute_rows expecting raw message rows.",
}

// variadicIndex matches the editor's `port[N]` convention for variadic
// input ports (e.g. attachments[0]). The engine resolves these to the
// base variadic port at run time; manifest validation only knows the
// base name, so we normalize before validating.
var variadicIndex = regexp.MustCompile(`\[\d+\]$`)

// normalizeVariadicPorts rewrites edge ToPorts of the form name[N] to
// name when the destination's base port exists and is variadic, so a
// graph the editor produces (and the engine runs) isn't flagged as a
// phantom "no such port".
func normalizeVariadicPorts(g *core.Graph, manifests map[string]core.Manifest) {
	mod := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		mod[n.ID] = n.Module
	}
	for i := range g.Edges {
		tp := g.Edges[i].ToPort
		loc := variadicIndex.FindStringIndex(tp)
		if loc == nil {
			continue
		}
		base := tp[:loc[0]]
		if m, ok := manifests[mod[g.Edges[i].To]]; ok {
			if p, ok := m.Input(base); ok && p.Variadic {
				g.Edges[i].ToPort = base
			}
		}
	}
}

// The shipped templates in web/public/templates are the non-technical
// entry point: a newcomer forks one and runs it. So they deserve the
// same composition guard as the scenario graphs — every module/port
// exists, wiring is type-compatible, required inputs are connected, and
// each for_each names a real step module. A failure here means a user
// who forks that template hits a runtime error they can't diagnose.
func TestShippedTemplatesCompose(t *testing.T) {
	manifests := combinedManifests(t)

	dir := filepath.Join("..", "..", "web", "public", "templates")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found under %s: %v", dir, err)
	}

	for _, f := range files {
		if filepath.Base(f) == "index.json" {
			continue // the gallery manifest, not a graph
		}
		t.Run(filepath.Base(f), func(t *testing.T) {
			if reason := knownBrokenTemplates[filepath.Base(f)]; reason != "" {
				t.Skipf("known broken: %s", reason)
			}
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var g core.Graph
			if err := json.Unmarshal(data, &g); err != nil {
				t.Fatalf("parse: %v", err)
			}
			normalizeVariadicPorts(&g, manifests)

			if err := core.ValidateWithManifests(g, manifests); err != nil {
				t.Errorf("template does not compose against the catalog:\n%v", err)
			}

			for _, n := range g.Nodes {
				if n.Module != "for_each" {
					continue
				}
				step, _ := n.Params["step_module"].(string)
				if step == "" {
					// Surface the likely cause: a stale `step` key.
					if _, hasStep := n.Params["step"]; hasStep {
						t.Errorf("for_each node %q uses the stale param %q; the engine reads %q, so this node fails at runtime",
							n.ID, "step", "step_module")
					} else {
						t.Errorf("for_each node %q has no step_module", n.ID)
					}
					continue
				}
				if step == "subgraph" {
					continue
				}
				if _, ok := manifests[step]; !ok {
					t.Errorf("for_each node %q references unknown step_module %q", n.ID, step)
				}
			}

			// A forked template should not ship a hard secret literal in
			// params (the lint the editor runs). Cheap to check here too.
			for _, n := range g.Nodes {
				raw, _ := json.Marshal(n.Params)
				if strings.Contains(string(raw), "sk-ant-") || strings.Contains(string(raw), "xoxb-") {
					t.Errorf("template node %q appears to hardcode a secret", n.ID)
				}
			}
		})
	}
}
