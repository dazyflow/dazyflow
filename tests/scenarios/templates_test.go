// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/rendertext"
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

// wholeReference matches a setting that is exactly one ${scheme.path}
// reference — a loop item's field, a secret, a resource.
var wholeReference = regexp.MustCompile(`^\s*\$\{[a-z0-9_-]+\.[^}]*\}\s*$`)

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
				wired := map[string]bool{}
				for _, e := range g.Edges {
					if e.To == n.ID {
						wired[e.ToPort] = true
					}
				}
				for _, issue := range paramSchemaIssues(n.Params, m.ParamsSchema, wired) {
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

// A render_text step's `template` is a CEL expression, and nothing above
// compiles it: ValidateWithManifests checks wiring, paramSchemaIssues checks
// that the param is a string. So a template shipping an expression with a typo
// — an unbalanced quote, a stray operator — composed cleanly and then failed at
// run time with "bad_param", which is precisely the "runtime error they can't
// diagnose" this file exists to prevent. Every template shipping a render_text
// now has its expression compiled.
//
// It is compiled, not evaluated for a result: the row a real run supplies comes
// from a Gmail message or a form submission, which this test has no way to
// synthesize faithfully. So a missing field on the probe row (an EvalError) is
// expected and ignored — only a ParseError, which is a defect in the shipped
// expression whatever data arrives, fails the test.
func TestShippedTemplateExpressionsCompile(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "public", "templates")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found under %s: %v", dir, err)
	}
	checked := 0
	for _, f := range files {
		if filepath.Base(f) == "index.json" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		var g core.Graph
		if err := json.Unmarshal(data, &g); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		for _, n := range g.Nodes {
			if n.Module != "render_text" {
				continue
			}
			if _, ok := n.Params["template"].(string); !ok {
				continue // a `column` renderer has no expression to compile
			}
			checked++
			probe := []map[string]any{{"probe": ""}}
			_, err := rendertext.Render(context.Background(), rendertext.SpecFromParams(n.Params), probe, 0)
			var pe *rendertext.ParseError
			if errors.As(err, &pe) {
				t.Errorf("%s: node %q has a template that does not compile: %v",
					filepath.Base(f), n.ID, pe)
			}
		}
	}
	if checked == 0 {
		t.Error("no render_text templates were compiled — the glob or the module name is wrong, so this guard is passing vacuously")
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
//
// wired names the node's connected input ports. A required setting whose
// matching input is wired is satisfied — that is the product's own model
// ("fill it in, or connect it"; a connected input overrides the typed
// value), so demanding a placeholder param alongside the wire would push
// templates into carrying values that the run ignores.
func paramSchemaIssues(params map[string]any, schema json.RawMessage, wired map[string]bool) []string {
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
		if _, ok := params[req]; ok {
			continue
		}
		if wired[req] {
			continue
		}
		issues = append(issues, fmt.Sprintf("missing required param %q (and no input wired to %q)", req, req))
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
// are float64) matches a JSON-Schema primitive type. A setting that is
// exactly one ${...} reference is a string in the JSON whatever the field's
// declared type — the engine resolves it at run time, and a whole-value
// reference keeps the real shape — so it satisfies any type.
func jsonTypeMatches(t string, v any) bool {
	if s, isStr := v.(string); isStr && wholeReference.MatchString(s) {
		return true
	}
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
