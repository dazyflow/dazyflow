// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package usecases validates the ten most-requested use cases in
// README.md against the real native catalog. Each NN-*.json is a graph
// that implements one use case as a non-technical buyer would describe it;
// the test puts every graph through the same authoring gate the product uses
// when a flow is saved (core.ValidateGraphFull) plus a param-level check the
// gate doesn't cover: unknown settings, unsatisfied required settings, and
// the declared type/enum of each setting.
//
// A failure here means one of the ten asks can no longer be built with
// shipped steps — that is the gap to close. Sibling suite: tests/scenarios,
// which covers recurring internal jobs rather than inbound asks.
package usecases

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops" // register every native drop
	"github.com/dazyflow/dazyflow/engine"
)

// catalog is the shipped drop catalog with the universal `pass` pin applied
// exactly as the engine's resolver does at run time, so a graph that
// legitimately sequences through a pass pin isn't flagged for a missing port.
func catalog() map[string]core.Manifest {
	out := map[string]core.Manifest{}
	for id, m := range engine.Default.Manifests() {
		out[id] = core.WithPassthrough(m)
	}
	return out
}

// paramSchema is the slice of JSON Schema the drops actually use.
type paramSchema struct {
	Type       string                  `json:"type"`
	Enum       []any                   `json:"enum"`
	Properties map[string]*paramSchema `json:"properties"`
	Required   []string                `json:"required"`
	Items      *paramSchema            `json:"items"`
}

// wholeReference matches a setting that is exactly one ${scheme.path}
// reference — an item field inside a loop body, a secret, a resource.
var wholeReference = regexp.MustCompile(`^\s*\$\{[a-z0-9_-]+\.[^}]*\}\s*$`)

// checkValue reports type/enum/required violations of v against s.
// Best-effort: an absent or unmodelled type is not an error.
func checkValue(path string, s *paramSchema, v any) []string {
	if s == nil {
		return nil
	}
	// A setting whose whole value is a ${…} reference is a string in the
	// graph JSON no matter what the field's declared type is — the engine
	// resolves it at run time, and a whole-value reference keeps the real
	// shape (a list stays a list). So it satisfies any type.
	if str, isStr := v.(string); isStr && wholeReference.MatchString(str) {
		return nil
	}
	var out []string
	switch s.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return []string{fmt.Sprintf("%s: expected text, got %T", path, v)}
		}
	case "integer", "number":
		if _, ok := v.(float64); !ok {
			return []string{fmt.Sprintf("%s: expected a number, got %T", path, v)}
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return []string{fmt.Sprintf("%s: expected true/false, got %T", path, v)}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected a list, got %T", path, v)}
		}
		for i, el := range arr {
			out = append(out, checkValue(fmt.Sprintf("%s[%d]", path, i), s.Items, el)...)
		}
		return out
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: expected an object, got %T", path, v)}
		}
		for _, r := range s.Required {
			if _, has := obj[r]; !has {
				out = append(out, fmt.Sprintf("%s: missing %q", path, r))
			}
		}
		for k, sub := range s.Properties {
			if val, has := obj[k]; has {
				out = append(out, checkValue(path+"."+k, sub, val)...)
			}
		}
		return out
	}
	if len(s.Enum) > 0 {
		for _, e := range s.Enum {
			if fmt.Sprint(e) == fmt.Sprint(v) {
				return nil
			}
		}
		out = append(out, fmt.Sprintf("%s: %v is not one of %v", path, v, s.Enum))
	}
	return out
}

func TestUseCaseGraphsValidate(t *testing.T) {
	manifests := catalog()

	files, err := filepath.Glob("*.json")
	if err != nil {
		t.Fatalf("glob use-case graphs: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no use-case graph JSON files found")
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
			// data-model migration the daemon applies on load.
			g = core.MigrateGraph(g)

			// The authoring gate: everything the app itself refuses to save.
			for _, is := range core.ValidateGraphFull(g, manifests) {
				if is.Severity == core.LintWarn {
					t.Logf("warn [%s] %s %v", is.Code, is.Message, is.NodeIDs)
					continue
				}
				t.Errorf("%s [%s] %s %v", is.Severity, is.Code, is.Message, is.NodeIDs)
			}

			for _, n := range g.Nodes {
				m, ok := manifests[n.Module]
				if !ok {
					continue // ValidateGraphFull already reported it
				}
				var root paramSchema
				if err := json.Unmarshal(m.ParamsSchema, &root); err != nil {
					continue
				}
				var unknown []string
				for k, v := range n.Params {
					sub, declared := root.Properties[k]
					if !declared {
						unknown = append(unknown, k)
						continue
					}
					for _, msg := range checkValue(k, sub, v) {
						t.Errorf("node %q (%s): %s", n.ID, n.Module, msg)
					}
				}
				sort.Strings(unknown)
				for _, k := range unknown {
					t.Errorf("node %q (%s): no such setting %q", n.ID, n.Module, k)
				}
				// A required setting may instead be satisfied by a wired input.
				wired := map[string]bool{}
				for _, e := range g.Edges {
					if e.To == n.ID {
						wired[e.ToPort] = true
					}
				}
				for _, r := range root.Required {
					if _, has := n.Params[r]; !has && !wired[r] {
						t.Errorf("node %q (%s): required setting %q is neither set nor wired", n.ID, n.Module, r)
					}
				}
			}

			// A for_each with no body wired has nothing to run.
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
