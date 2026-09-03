// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build ignore

// Prints every drop id in the catalog with the English text the translations
// are made from — its name, its wiring pins, its description, and every string
// its params_schema and its connection fields put on a screen — as JSON. The
// web build's Swedish coverage guard reads the result, so adding a drop,
// adding a port, adding a param, or rewording any of them fails that test
// until the Swedish is written or refreshed.
//
// Why the description TEXT and not just the ids: i18n/drops/descriptions.sv.ts
// records the FINGERPRINT of the English each translation was made from, and
// falls back to English when it stops matching. That fail-safe is deliberate,
// but it is also silent — a reworded paragraph quietly reverts a Swedish
// reader to English with nothing anywhere saying so. Shipping the English here
// lets the guard recompute the fingerprint and name the drops that have gone
// stale.
//
// Why the PORTS: same silence, one word at a time. A pin whose label has no
// SV_PORTS entry renders its English on the card, next to pins that read
// Swedish, and nothing fails. Shipping the labels lets the guard name the drop
// the untranslated pin belongs to instead of just the orphan string.
//
// Why the rest — the label, the subtitle, the params_schema titles, help and
// enumNames, the connection fields, the "keeps state" copy: every one of them
// is looked up the same forgiving way in dropText.ts, so all of them fail
// silently too, and they had: 207 strings needed writing or re-keying — 127
// field-help paragraphs, 25 field titles, 24 connection strings and the rest
// spread over the names, the dropdowns and the keeps-state copy. Mostly whole
// drop families added after the last translation pass, plus the fallout of a
// 'wire' → 'connect' rewording that orphaned 38 translations at once. What is
// not shipped here is not guarded, which is how that happened.
//
// Ports go through core.WithPassthrough so the list is what the canvas draws,
// not what the drop declared: the universal pass pin is a label a reader sees
// and therefore a label that needs translating.
//
//	go run ./scripts/droptext.go > web/src/i18n/drops/catalog.json
package main

import (
	"encoding/json"
	"os"
	"sort"

	"github.com/dazyflow/dazyflow/core"
	_ "github.com/dazyflow/dazyflow/drops"
	"github.com/dazyflow/dazyflow/engine"
)

type entry struct {
	Description string `json:"description"`
	// Sorted and de-duplicated: a drop with three pins reading "Text" should
	// present the translator with one string, and the file should not churn
	// because a port literal moved. Every list below is built the same way.
	Ports []string `json:"ports"`
	// The card's own words: Label is the step's name, Subtitle the action line
	// under it, Integration the app chip the palette and the Inspector show.
	Label       string `json:"label"`
	Subtitle    string `json:"subtitle,omitempty"`
	Integration string `json:"integration,omitempty"`
	// The params-schema surface, one list per kind of string, matching the
	// resolver each one goes through (fieldTitle / fieldHelp / enumLabel). A
	// field titled "Status" and a dropdown option "Status" are different
	// strings to a translator even when they match today, so they are not
	// merged here either.
	Titles    []string `json:"titles,omitempty"`
	Help      []string `json:"help,omitempty"`
	EnumNames []string `json:"enum_names,omitempty"`
	// The connection card on the app's page: field labels, the help under each
	// input, and the placeholders inside them (localized too — several are
	// prose, e.g. "usually your email address").
	Connection []string `json:"connection,omitempty"`
	// The "keeps state" chip on a node card and its reset explanation.
	NodeState []string `json:"node_state,omitempty"`
	// Secret-kind connection notes, verbatim. The Apps page splits each one
	// into a field label and an example value ("Notion integration token
	// (secret_…)") and localizes the label half, so the guard needs the whole
	// note to apply the same split. OAuth notes are absent: that card names
	// the provider, never the note.
	SecretNotes []string `json:"secret_notes,omitempty"`
}

// collect appends to a de-duplicating set.
func collect(seen map[string]bool, out []string, values ...string) []string {
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// walkSchema pulls every human string out of a params_schema: the title and
// description on each property, and the enumNames beside an enum. It recurses
// through the places a schema nests, because a field inside an object or a
// table's row schema is still a field somebody reads.
func walkSchema(node any, e *entry, seen map[string]map[string]bool) {
	m, ok := node.(map[string]any)
	if !ok {
		return
	}
	if s, ok := m["title"].(string); ok {
		e.Titles = collect(seen["titles"], e.Titles, s)
	}
	if s, ok := m["description"].(string); ok {
		e.Help = collect(seen["help"], e.Help, s)
	}
	if arr, ok := m["enumNames"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				e.EnumNames = collect(seen["enums"], e.EnumNames, s)
			}
		}
	}
	for _, key := range []string{"properties", "definitions", "$defs", "patternProperties"} {
		if props, ok := m[key].(map[string]any); ok {
			for _, v := range props {
				walkSchema(v, e, seen)
			}
		}
	}
	for _, key := range []string{"items", "additionalProperties", "if", "then", "else"} {
		if v, ok := m[key]; ok {
			walkSchema(v, e, seen)
		}
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if arr, ok := m[key].([]any); ok {
			for _, v := range arr {
				walkSchema(v, e, seen)
			}
		}
	}
}

func main() {
	// map[id]entry — encoding/json sorts map keys, so re-runs produce clean
	// diffs the same way docsgen's output does.
	out := map[string]entry{}
	for _, m := range engine.Default.Manifests() {
		m = core.WithPassthrough(m)
		e := entry{
			Description: m.Description,
			Label:       m.Label,
			Subtitle:    m.Subtitle,
			Integration: m.Integration,
		}
		seen := map[string]map[string]bool{
			"ports": {}, "titles": {}, "help": {}, "enums": {}, "connection": {},
			"state": {}, "notes": {},
		}
		for _, p := range append(append([]core.Port{}, m.Inputs...), m.Outputs...) {
			e.Ports = collect(seen["ports"], e.Ports, p.Label)
		}
		if len(m.ParamsSchema) > 0 {
			var root any
			if err := json.Unmarshal(m.ParamsSchema, &root); err != nil {
				panic(err)
			}
			walkSchema(root, &e, seen)
		}
		for _, f := range m.ConnectionFields {
			e.Connection = collect(seen["connection"], e.Connection, f.Label, f.Help, f.Placeholder)
			e.Connection = collect(seen["connection"], e.Connection, f.Options...)
		}
		for _, r := range m.RequiresConnections {
			if r.Kind == "secret" {
				e.SecretNotes = collect(seen["notes"], e.SecretNotes, r.Note)
			}
		}
		if m.NodeState != nil {
			e.NodeState = collect(seen["state"], e.NodeState, m.NodeState.Label, m.NodeState.ResetHint)
		}
		for _, list := range [][]string{
			e.Ports, e.Titles, e.Help, e.EnumNames, e.Connection, e.NodeState,
			e.SecretNotes,
		} {
			sort.Strings(list)
		}
		out[m.ID] = e
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
}
