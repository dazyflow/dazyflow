// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import "testing"

// TestPortKindDerivation pins the MIME → simplified-model mapping (Phase 1 of
// the data-layer simplification). Manifests are unchanged; Kind() is the bridge
// tooling/engine/UI read instead of pattern-matching MIME.
func TestPortKindDerivation(t *testing.T) {
	cases := []struct {
		name string
		port Port
		want PortKind
	}{
		{"untyped is any", Port{Port: "pass"}, KindAny},
		{"json is item", Port{Port: "rows", MIME: []string{"application/json"}}, KindItem},
		{"list+json is item", Port{Port: "rows", MIME: []string{"application/json"}, List: true}, KindItem},
		{"loop results is item", Port{Port: "results", MIME: []string{"application/x-dazyflow-list+json"}}, KindItem},
		{"text/plain is text", Port{Port: "text", MIME: []string{"text/plain"}}, KindText},
		{"html is text", Port{Port: "html", MIME: []string{"text/html", "text/plain"}}, KindText},
		{"bool is bool", Port{Port: "ok", MIME: []string{"application/x-bool"}}, KindBool},
		{"json+text sink classifies as item", Port{Port: "in", MIME: []string{"application/json", "text/plain"}}, KindItem},
		{"pdf is file", Port{Port: "doc", MIME: []string{"application/pdf"}}, KindFile},
		{"spreadsheet is file", Port{Port: "xlsx", MIME: []string{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}}, KindFile},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.port.Kind(); got != c.want {
				t.Fatalf("Kind() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestPortCardinality(t *testing.T) {
	if (Port{}).Cardinality() != One {
		t.Errorf("non-list port should be One")
	}
	if (Port{List: true}).Cardinality() != Many {
		t.Errorf("list port should be Many")
	}
}
