// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"testing"

	"github.com/dazyflow/dazyflow/core"
)

func catalog() map[string]core.Manifest {
	return map[string]core.Manifest{
		"sleep": {
			ID: "sleep", Label: "Sleep", Category: "flow_control",
			Provider: "internal", Tags: []string{"timing", "delay"},
			Description: "Pause for a configurable duration.",
		},
		"branch": {
			ID: "branch", Label: "Branch", Category: "flow_control",
			Provider: "internal", Tags: []string{"conditional", "routing"},
			Description: "Route input based on a structured condition.",
		},
		"file_read": {
			ID: "file_read", Label: "File read", Category: "io",
			Provider: "internal", Tags: []string{"filesystem", "read"},
			Description: "Read a file from the workspace sandbox.",
		},
		"file_write": {
			ID: "file_write", Label: "File write", Category: "io",
			Provider: "internal", Tags: []string{"filesystem", "write"},
			Description: "Write a file to the workspace sandbox.",
		},
		"http_request": {
			ID: "http_request", Label: "HTTP request", Category: "network",
			Provider: "internal", Tags: []string{"http", "rest", "api"},
			Description: "Make an HTTP request to any URL with SSRF defaults.",
		},
		"claude": {
			ID: "claude", Label: "Claude (Anthropic Messages API)", Category: "ai",
			Provider: "anthropic", Tags: []string{"llm", "anthropic"},
			Description: "Call Anthropic's Messages API with a one-shot prompt.",
		},
		"mcp:slack:post_message": {
			ID: "mcp:slack:post_message", Label: "slack — post_message", Category: "external",
			Provider: "mcp:slack", Tags: []string{"mcp", "slack"},
			Description: "Post a message to a Slack channel via MCP.",
		},
		"mcp:fs:read_file": {
			ID: "mcp:fs:read_file", Label: "fs — read_file", Category: "external",
			Provider: "mcp:fs", Tags: []string{"mcp", "fs"},
			Description: "Read a file via the filesystem MCP server.",
		},
	}
}

func TestSearch_NoFiltersReturnsAllAlphabetical(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{})
	if len(got) != 8 {
		t.Fatalf("got %d, want 8", len(got))
	}
	prev := ""
	for _, m := range got {
		if m.ID < prev {
			t.Errorf("not sorted: %q < %q", m.ID, prev)
		}
		prev = m.ID
	}
}

func TestSearch_FilterByCategory(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Categories: []string{"flow_control"}})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	for _, m := range got {
		if m.Category != "flow_control" {
			t.Errorf("unexpected category %q", m.Category)
		}
	}
}

func TestSearch_FilterByProvider(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Providers: []string{"mcp:slack"}})
	if len(got) != 1 || got[0].ID != "mcp:slack:post_message" {
		t.Errorf("got %+v", got)
	}
}

func TestSearch_FilterByMultipleProvidersOR(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{
		Providers: []string{"anthropic", "mcp:slack"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestSearch_FilterByTagAnySemantics(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Tags: []string{"filesystem"}})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (file_read + file_write)", len(got))
	}
}

func TestSearch_FiltersANDAcrossFields(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{
		Categories: []string{"ai"},
		Providers:  []string{"anthropic"},
	})
	if len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("got %+v", got)
	}
}

func TestSearch_QueryExactID(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "sleep"})
	if len(got) < 1 || got[0].ID != "sleep" {
		t.Errorf("expected sleep first, got %+v", got)
	}
}

func TestSearch_QueryPartialMatchOnDescription(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "sandbox"})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (file_read + file_write match 'sandbox'); got %v",
			len(got), idsOf(got))
	}
}

func TestSearch_QueryMatchesTags(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "llm"})
	if len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("got %v, want [claude]", idsOf(got))
	}
}

func TestSearch_QueryRelevanceRanking(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "file"})
	if len(got) < 2 {
		t.Fatalf("expected ≥2 matches; got %d", len(got))
	}
	first := got[0].ID
	if first != "file_read" && first != "file_write" {
		t.Errorf("first match = %q; expected file_read or file_write", first)
	}
}

func TestSearch_NoMatchReturnsEmpty(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "qwertyzz-nothing-matches"})
	if len(got) != 0 {
		t.Errorf("got %d matches for nonsense query", len(got))
	}
}

func TestSearch_CombinedQueryAndFilter(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{
		Query:      "file",
		Categories: []string{"external"},
	})
	if len(got) != 1 || got[0].ID != "mcp:fs:read_file" {
		t.Errorf("got %v, want [mcp:fs:read_file]", idsOf(got))
	}
}

func TestSearch_QueryCaseInsensitive(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Query: "HTTP"})
	if len(got) != 1 || got[0].ID != "http_request" {
		t.Errorf("got %v, want [http_request]", idsOf(got))
	}
}

func TestSearch_FiltersCaseInsensitive(t *testing.T) {
	t.Parallel()
	got := searchManifests(catalog(), DropSearch{Providers: []string{"INTERNAL"}})
	if len(got) != 5 {
		t.Errorf("got %d, want 5", len(got))
	}
}

func TestSearch_MatchScore_ExactBeatsPartial(t *testing.T) {
	t.Parallel()
	m := core.Manifest{ID: "claude", Description: "claude is a chatbot"}
	exact := matchScore(m, "claude")
	if exact < 100 {
		t.Errorf("exact ID score = %d; want >= 100", exact)
	}
}

func TestSearch_SearchBoostBreaksTie(t *testing.T) {
	t.Parallel()
	// Two drops that match "save" identically by tag; the boosted one
	// must rank first regardless of the alphabetical tie-break (which
	// would otherwise put "a_store" ahead of "z_sqlite").
	cat := map[string]core.Manifest{
		"a_store": {
			ID: "a_store", Label: "Collections", Category: "io",
			Provider: "internal", Tags: []string{"save", "store"},
			Description: "Save rows with no setup.",
		},
		"z_sqlite": {
			ID: "z_sqlite", Label: "SQLite", Category: "io",
			Provider: "internal", Tags: []string{"save", "database"},
			Description: "Save rows to a database file.", SearchBoost: 25,
		},
	}
	got := idsOf(searchManifests(cat, DropSearch{Query: "save"}))
	if len(got) != 2 || got[0] != "z_sqlite" {
		t.Fatalf("save ranking = %v; want boosted z_sqlite first", got)
	}

	// A negative boost down-ranks but must NOT drop the match entirely.
	m := core.Manifest{ID: "x", Description: "save", Tags: []string{"save"}, SearchBoost: -1000}
	if s := matchScore(m, "save"); s < 1 {
		t.Errorf("negative boost dropped the match: score=%d, want >=1", s)
	}

	// Boost never lets a fuzzy match overtake an exact-ID hit.
	exact := matchScore(core.Manifest{ID: "save"}, "save")
	boostedFuzzy := matchScore(core.Manifest{ID: "other", Tags: []string{"save"}, SearchBoost: 100}, "save")
	if boostedFuzzy >= exact {
		t.Errorf("boosted fuzzy (%d) overtook exact ID (%d)", boostedFuzzy, exact)
	}
}

func idsOf(ms []core.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestSearch_MultiWordQueryMatchesAnyWord(t *testing.T) {
	t.Parallel()
	// AND-across-words returned nothing here: no manifest contains the phrase
	// "read file", and requiring both words of a two-word query is what made
	// every phrase a dead end.
	got := searchManifests(catalog(), DropSearch{Query: "read file"})
	if len(got) == 0 {
		t.Fatal("multi-word query returned nothing")
	}
	if got[0].ID != "file_read" && got[0].ID != "mcp:fs:read_file" {
		t.Errorf("first match = %q; want a file-read step", got[0].ID)
	}
}

func TestSearch_MoreWordsMatchedRanksHigher(t *testing.T) {
	t.Parallel()
	// http_request matches both words; sleep matches neither; branch matches
	// only "request" (through "Route input based on a structured condition"
	// it does not, so it should be absent entirely).
	got := searchManifests(catalog(), DropSearch{Query: "http request"})
	if len(got) == 0 || got[0].ID != "http_request" {
		t.Fatalf("got %v, want http_request first", idsOf(got))
	}
}

func TestSearch_ShortWordsIgnoredInPhrase(t *testing.T) {
	t.Parallel()
	// "to" and "a" match a great deal and mean nothing; the result must be
	// driven by "file" alone.
	got := searchManifests(catalog(), DropSearch{Query: "write to a file"})
	if len(got) == 0 || got[0].ID != "file_write" {
		t.Fatalf("got %v, want file_write first", idsOf(got))
	}
}

func TestSearch_ProseMatchesWholeWordsOnly(t *testing.T) {
	t.Parallel()
	// The bug this pins: as a substring, "form" matched "format" in a summary,
	// so a search for it ranked formatting steps above the Form trigger.
	formatter := core.Manifest{
		ID: "build_csv", Label: "Build CSV",
		Summary: "Format rows as CSV.", Description: "Formats rows.",
	}
	if s := matchScore(formatter, "form"); s != 0 {
		t.Errorf(`matchScore(build_csv, "form") = %d; want 0 — "format" is not "form"`, s)
	}
	trigger := core.Manifest{
		ID: "form_input", Label: "Form",
		Summary: "Starts the flow when someone submits the form.",
	}
	if matchScore(trigger, "form") <= matchScore(formatter, "form") {
		t.Error("form_input must outrank a formatting step for \"form\"")
	}
}

func TestSearch_ContainsWordBoundaries(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		text, word string
		want       bool
	}{
		{"format rows as csv", "form", false},
		{"transform the rows", "form", false},
		{"submits the form.", "form", true},
		{"a form, hosted", "form", true},
		{"form-input trigger", "form", true},
		{"the forms are hosted", "form", false},
		{"", "form", false},
		{"form", "", false},
	} {
		if got := containsWord(c.text, c.word); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v; want %v", c.text, c.word, got, c.want)
		}
	}
}
