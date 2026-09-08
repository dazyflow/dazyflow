// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package svsearch

import (
	"slices"
	"strings"
	"testing"
)

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"E-Post":     "epost",
		"e post":     "epost",
		"epost":      "epost",
		"fördröj":    "fordroj",
		"återkommer": "aterkommer",
		"BERÄKNA":    "berakna",
		"":           "",
		"!!!":        "",
	} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestExpand(t *testing.T) {
	for _, c := range struct2(
		"faktura", "invoice",
		"fakturorna", "invoice", // inflected: -orna stripped
		"kalkylark", "spreadsheet",
		"e-post", "email",
		"epost", "email",
		"frakt", "shipping",
		"organisationsnummer", "org-number",
		"fakturamall", "invoice", // compound read through its head word
	) {
		got := Expand(c.in)
		if !slices.Contains(got, c.want) {
			t.Errorf("Expand(%q) = %v; want it to contain %q", c.in, got, c.want)
		}
	}
}

// An English catalogue word must have no Swedish reading, or every English
// query pays for the table.
func TestExpand_EnglishWordsHaveNoReading(t *testing.T) {
	for _, w := range []string{"email", "slack", "invoice", "sheets", "webhook", "rows"} {
		if got := Expand(w); len(got) > 0 {
			t.Errorf("Expand(%q) = %v; an English catalogue word should expand to nothing", w, got)
		}
	}
}

// Values must be words the catalogue actually contains, or an alias points at
// nothing. Checked here as a shape rule — lower case, no stray punctuation —
// since this package cannot see the manifests.
func TestAliases_ValuesLookLikeCatalogueTerms(t *testing.T) {
	for k, terms := range Aliases {
		if k != strings.ToLower(k) {
			t.Errorf("alias key %q is not lower case", k)
		}
		if Fold(k) == "" {
			t.Errorf("alias key %q folds to nothing, so it can never be looked up", k)
		}
		if len(terms) == 0 {
			t.Errorf("alias key %q expands to nothing", k)
		}
		for _, term := range terms {
			if term != strings.ToLower(term) {
				t.Errorf("alias %q → %q is not lower case", k, term)
			}
			if strings.TrimSpace(term) != term || term == "" {
				t.Errorf("alias %q → %q has stray whitespace", k, term)
			}
		}
	}
}

type pair struct{ in, want string }

func struct2(ss ...string) []pair {
	out := make([]pair, 0, len(ss)/2)
	for i := 0; i+1 < len(ss); i += 2 {
		out = append(out, pair{ss[i], ss[i+1]})
	}
	return out
}
