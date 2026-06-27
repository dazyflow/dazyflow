// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package scenarios

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"git.sr.ht/~klahr/dazyflow/core"
)

// knownBrokenTemplates are shipped templates with a real defect that
// predates this guard, with the finding noted. They're skipped (loudly)
// rather than silently passing, so the gap stays visible until fixed.
var knownBrokenTemplates = map[string]string{}

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
// each for_each has its `body` pin wired. A failure here means a user
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
			g = core.MigrateGraph(g) // validate post-migration, as the daemon loads it
			normalizeVariadicPorts(&g, manifests)

			if err := core.ValidateWithManifests(g, manifests); err != nil {
				t.Errorf("template does not compose against the catalog:\n%v", err)
			}

			// Every for_each must have its `body` pin wired to a loop body —
			// an unwired for_each has nothing to run. The body nodes' modules
			// and ports are already validated by ValidateWithManifests above.
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

			// A forked template should not ship a hard secret literal in
			// params (the lint the editor runs). Cheap to check here too.
			for _, n := range g.Nodes {
				raw, _ := json.Marshal(n.Params)
				if strings.Contains(string(raw), "sk-ant-") || strings.Contains(string(raw), "xoxb-") {
					t.Errorf("template node %q appears to hardcode a secret", n.ID)
				}
			}

			// Params must satisfy each drop's declared schema. The
			// composition check above validates wiring but NOT params, so
			// before this a missing/typo'd required param (e.g. `conflict`
			// where the drop requires `conflict_columns`) sailed straight
			// through and only blew up at run time.
			for _, n := range g.Nodes {
				m, ok := manifests[n.Module]
				if !ok {
					continue // the composition check already flagged the unknown module
				}
				for _, issue := range paramSchemaIssues(n.Params, m.ParamsSchema) {
					t.Errorf("node %q (%s): %s", n.ID, n.Module, issue)
				}
			}

			// A scalar-string sink port (a chat/email/issue body, a stored
			// value) must be fed a rendered string — text/plain — not a
			// rows list. Wiring compute_rows.rows straight into
			// slack_send_message.body is the antipattern that fails at run
			// time with "structured value… render it as a string";
			// render_text.text is the fix. The composition check is blind
			// to this because the sink ports declare no MIME of their own.
			mod := make(map[string]string, len(g.Nodes))
			for _, n := range g.Nodes {
				mod[n.ID] = n.Module
			}
			for _, e := range g.Edges {
				if !stringSinkPorts[mod[e.To]][e.ToPort] {
					continue
				}
				src, ok := manifests[mod[e.From]]
				if !ok {
					continue
				}
				p, ok := src.Output(e.FromPort)
				if !ok || len(p.MIME) == 0 {
					continue // an untyped source could legitimately carry a string
				}
				if !slices.Contains(p.MIME, "text/plain") {
					t.Errorf("node %q feeds %s.%s from %s.%s (%v) — that port wants a rendered string; route it through render_text and wire its text output",
						e.From, mod[e.To], e.ToPort, mod[e.From], e.FromPort, p.MIME)
				}
			}
		})
	}
}

// stringSinkPorts are (module, input-port) pairs that consume a single
// rendered string rather than a rows list. The drops behind them read
// the value as text and either JSON-dump or hard-fail on a structured
// value, so a template must feed them text/plain (render_text.text),
// never an application/json rows stream.
var stringSinkPorts = map[string]map[string]bool{
	"slack_send_message":  {"body": true},
	"gmail_send_email":    {"body": true},
	"github_create_issue": {"body": true},
	"secret_set":          {"value": true},
}

// paramSchemaIssues does a focused check of a node's params against the
// drop's JSON-Schema: required properties are present, and declared
// properties carry a value of the declared JSON type. It is deliberately
// not a full validator — just enough to catch the param mistakes the
// composition check can't see. It only inspects declared properties, so
// a drop whose schema omits a param it accepts won't produce a false
// positive.
func paramSchemaIssues(params map[string]any, schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var s struct {
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil // a schema we can't parse isn't the template's fault
	}
	var issues []string
	for _, req := range s.Required {
		if _, ok := params[req]; !ok {
			issues = append(issues, fmt.Sprintf("missing required param %q", req))
		}
	}
	for name, prop := range s.Properties {
		v, ok := params[name]
		if !ok || prop.Type == "" {
			continue
		}
		if !jsonTypeMatches(prop.Type, v) {
			issues = append(issues, fmt.Sprintf("param %q should be %s, got %T", name, prop.Type, v))
		}
	}
	return issues
}

// jsonTypeMatches reports whether v (decoded by encoding/json, so numbers
// are float64) matches a JSON-Schema primitive type. A ${...} placeholder
// is always a string, which is what schema-typed string params expect, so
// no special-casing is needed for the values templates actually carry.
func jsonTypeMatches(t string, v any) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number", "integer":
		_, ok := v.(float64)
		return ok
	}
	return true // unknown or unconstrained type: don't second-guess
}
