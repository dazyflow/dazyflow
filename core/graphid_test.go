// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package core

import (
	"strings"
	"testing"
)

// A flow ID names a file and a git ref component, so the shapes that used to
// slip through were a path escape and a ref-name injection at once.
func TestValidGraphID(t *testing.T) {
	valid := []string{
		"order-received-alert", // what the editor's slugify produces
		"flow",
		"invoice.v2",
		"my_flow_2",
		"A1",
		strings.Repeat("n", MaxGraphIDLen),
	}
	for _, id := range valid {
		if err := ValidGraphID(id); err != nil {
			t.Errorf("ValidGraphID(%q) = %v, want nil", id, err)
		}
	}

	invalid := map[string]string{
		"":                                   "empty",
		"a/../../escape":                     "saved outside graphs/ and could never be loaded again",
		"..":                                 "climbs out of graphs/",
		".":                                  "not a name",
		"-leading-dash":                      "must start with a letter or digit",
		".hidden":                            "a git ref component may not start with a dot",
		"with space":                         "a git ref may not contain a space, so it could never be published",
		"with\nnewline":                      "control character",
		"slash/inside":                       "path separator",
		"pct%2fescape":                       "percent-escaped separator",
		"a.lock":                             "a git ref component may not end in .lock",
		"trailing.":                          "trailing dot",
		"emoji💥":                             "not ASCII",
		strings.Repeat("n", MaxGraphIDLen+1): "too long for a filename",
	}
	for id, why := range invalid {
		if err := ValidGraphID(id); err == nil {
			t.Errorf("ValidGraphID(%q) = nil, want an error (%s)", id, why)
		}
	}
}
