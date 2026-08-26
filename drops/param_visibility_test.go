// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"encoding/json"
	"sort"
	"testing"
)

// x_visible_when hides a param until a sibling param has a particular value —
// the Date & time step's Custom format field, which is noise until Format is
// set to Custom.
//
// It is a pointer between two params written as a string, so nothing but this
// test connects the two ends. Misspell the sibling name or its value and the
// field simply never appears again: no error, no warning, and the form looks
// exactly like a field that was deliberately removed. That is the failure this
// guards, and it is the kind that ships.
//
// Also checked: a conditional field must not be in `required`. The form would
// hide it while the config checklist demanded it, leaving a red node with
// nothing to fill in. Conditional requirements belong in the drop's own
// Execute, which can say "Format is Custom but no format is set".
type visibilitySchema struct {
	Required   []string `json:"required"`
	Properties map[string]struct {
		Enum        []json.RawMessage          `json:"enum"`
		Default     json.RawMessage            `json:"default"`
		VisibleWhen map[string]json.RawMessage `json:"x_visible_when"`
	} `json:"properties"`
}

func TestVisibleWhenPointsAtRealSiblings(t *testing.T) {
	var problems []string
	for _, d := range allDrops(t) {
		if len(d.manifest.ParamsSchema) == 0 {
			continue
		}
		var s visibilitySchema
		if err := json.Unmarshal(d.manifest.ParamsSchema, &s); err != nil {
			continue // TestParamNaming_Conventions reports the parse failure
		}
		required := map[string]bool{}
		for _, r := range s.Required {
			required[r] = true
		}
		for name, prop := range s.Properties {
			if len(prop.VisibleWhen) == 0 {
				continue
			}
			if required[name] {
				problems = append(problems, d.id+"."+name+
					" is in `required` AND conditional — the form would hide a field the checklist demands; validate it in Execute instead")
			}
			for sibling, want := range prop.VisibleWhen {
				target, ok := s.Properties[sibling]
				if !ok {
					problems = append(problems, d.id+"."+name+
						": x_visible_when names \""+sibling+"\", which is not a param of this drop — the field would never show")
					continue
				}
				if len(target.Enum) == 0 {
					continue // a free-text sibling: any value is possible
				}
				if !enumContains(target.Enum, want) {
					problems = append(problems, d.id+"."+name+
						": x_visible_when wants "+sibling+"="+string(want)+
						", which is not one of that param's enum values — the field would never show")
				}
			}
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// enumContains reports whether want — a single JSON value or an array of them —
// is covered by the enum. Both spellings are accepted in x_visible_when, since
// "show when mode is either of these two" is a natural thing to want.
func enumContains(enum []json.RawMessage, want json.RawMessage) bool {
	var many []json.RawMessage
	if err := json.Unmarshal(want, &many); err != nil {
		many = []json.RawMessage{want}
	}
	for _, w := range many {
		var wv any
		if err := json.Unmarshal(w, &wv); err != nil {
			return false
		}
		found := false
		for _, e := range enum {
			var ev any
			if err := json.Unmarshal(e, &ev); err == nil && ev == wv {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
