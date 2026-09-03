// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package drops_test

import (
	"encoding/json"
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

// Port.Example is what a step says it produces BEFORE it has ever run: the
// editor renders it on the card's data face and the reference picker reads its
// field names, so a flow gets wired against it.
//
// That makes a wrong example worse than none. An example promising a field the
// port never carries teaches an author to write ${…total} against nothing, and
// the failure surfaces on the first real run — far from the cause. Nothing can
// statically prove the field NAMES right, but the shape is checkable, and a
// shape mismatch is the class of error that breaks the card outright.
//
// Companion to output_contract_test.go (the ports themselves are well-formed)
// and examples_contract_test.go (the params side).

// TestAllDrops_OutputExamplesMatchTheirPort asserts every declared output
// example is valid JSON and carries what its port's declared MIME and List
// flag promise.
func TestAllDrops_OutputExamplesMatchTheirPort(t *testing.T) {
	for _, d := range allDrops(t) {
		t.Run(d.id, func(t *testing.T) {
			for _, p := range d.manifest.Outputs {
				if len(p.Example) == 0 {
					continue // optional
				}
				var v any
				if err := json.Unmarshal(p.Example, &v); err != nil {
					t.Errorf("port %q: Example is not valid JSON: %v", p.Port, err)
					continue
				}
				checkExampleShape(t, p, v)
			}
		})
	}
}

// checkExampleShape holds the example to the port's own declaration.
//
// An array example is accepted on any port, and only its ELEMENTS are held to
// the port's Kind. That is deliberately looser than reading Cardinality():
// 44 output ports named "rows" carry an array and none of them declares
// List: true, so requiring a single object wherever List is unset would
// reject every correct example on the catalogue's commonest list port. The
// implication runs one way only — a port that DOES declare List must carry
// many, and that direction is enforced.
func checkExampleShape(t *testing.T, p core.Port, v any) {
	t.Helper()
	arr, isArray := v.([]any)
	if p.Cardinality() == core.Many && !isArray {
		t.Errorf("port %q: List port, so Example must be an array; got %T", p.Port, v)
		return
	}
	if !isArray {
		checkExampleElement(t, p, v, -1)
		return
	}
	if len(arr) == 0 {
		t.Errorf("port %q: Example is an empty array, which shows a reader nothing", p.Port)
		return
	}
	for i, el := range arr {
		checkExampleElement(t, p, el, i)
	}
}

func checkExampleElement(t *testing.T, p core.Port, el any, idx int) {
	t.Helper()
	where := p.Port
	if idx >= 0 {
		where = p.Port + "[" + itoa(idx) + "]"
	}
	switch p.Kind() {
	case core.KindItem:
		// An Items port carries records. A bare scalar in an Items example is
		// what makes the card render a column-less table.
		if _, ok := el.(map[string]any); !ok {
			t.Errorf("port %q: Items port, so Example must be a {field: value} object; got %T", where, el)
		}
	case core.KindText:
		s, ok := el.(string)
		if !ok {
			t.Errorf("port %q: Text port, so Example must be a string; got %T", where, el)
			return
		}
		if s == "" {
			t.Errorf("port %q: Example is the empty string, which shows a reader nothing", where)
		}
	case core.KindBool:
		if _, ok := el.(bool); !ok {
			t.Errorf("port %q: Yes/no port, so Example must be a boolean; got %T", where, el)
		}
	case core.KindFile:
		// A file's bytes are not an example anybody wants on a card; its name
		// is. Kept a string so the face shows "Faktura.pdf".
		if _, ok := el.(string); !ok {
			t.Errorf("port %q: File port, so Example must be a file name string; got %T", where, el)
		}
	}
}
