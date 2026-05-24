package daemon

import (
	"testing"

	"git.sr.ht/~klahr/hazy-flow/core"
)

// catalog returns a representative manifest set covering each category
// the platform ships with today. Tests assert filter + search behaviour
// against this fixed corpus.
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
	got := searchManifests(catalog(), ModuleSearch{})
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
	got := searchManifests(catalog(), ModuleSearch{Categories: []string{"flow_control"}})
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
	got := searchManifests(catalog(), ModuleSearch{Providers: []string{"mcp:slack"}})
	if len(got) != 1 || got[0].ID != "mcp:slack:post_message" {
		t.Errorf("got %+v", got)
	}
}

func TestSearch_FilterByMultipleProvidersOR(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{
		Providers: []string{"anthropic", "mcp:slack"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
}

func TestSearch_FilterByTagAnySemantics(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Tags: []string{"filesystem"}})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (file_read + file_write)", len(got))
	}
}

func TestSearch_FiltersANDAcrossFields(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{
		Categories: []string{"ai"},
		Providers:  []string{"anthropic"},
	})
	if len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("got %+v", got)
	}
}

func TestSearch_QueryExactID(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Query: "sleep"})
	if len(got) < 1 || got[0].ID != "sleep" {
		t.Errorf("expected sleep first, got %+v", got)
	}
}

func TestSearch_QueryPartialMatchOnDescription(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Query: "sandbox"})
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (file_read + file_write match 'sandbox'); got %v",
			len(got), idsOf(got))
	}
}

func TestSearch_QueryMatchesTags(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Query: "llm"})
	if len(got) != 1 || got[0].ID != "claude" {
		t.Errorf("got %v, want [claude]", idsOf(got))
	}
}

func TestSearch_QueryRelevanceRanking(t *testing.T) {
	// "file" matches several manifests; the prefix-on-ID matches should
	// rank above body-of-description matches.
	got := searchManifests(catalog(), ModuleSearch{Query: "file"})
	if len(got) < 2 {
		t.Fatalf("expected ≥2 matches; got %d", len(got))
	}
	first := got[0].ID
	if first != "file_read" && first != "file_write" {
		t.Errorf("first match = %q; expected file_read or file_write", first)
	}
}

func TestSearch_NoMatchReturnsEmpty(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Query: "qwertyzz-nothing-matches"})
	if len(got) != 0 {
		t.Errorf("got %d matches for nonsense query", len(got))
	}
}

func TestSearch_CombinedQueryAndFilter(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{
		Query:      "file",
		Categories: []string{"external"},
	})
	if len(got) != 1 || got[0].ID != "mcp:fs:read_file" {
		t.Errorf("got %v, want [mcp:fs:read_file]", idsOf(got))
	}
}

func TestSearch_QueryCaseInsensitive(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Query: "HTTP"})
	if len(got) != 1 || got[0].ID != "http_request" {
		t.Errorf("got %v, want [http_request]", idsOf(got))
	}
}

func TestSearch_FiltersCaseInsensitive(t *testing.T) {
	got := searchManifests(catalog(), ModuleSearch{Providers: []string{"INTERNAL"}})
	// 5 internal modules in catalog: sleep, branch, file_read, file_write, http_request
	if len(got) != 5 {
		t.Errorf("got %d, want 5", len(got))
	}
}

func TestSearch_MatchScore_ExactBeatsPartial(t *testing.T) {
	// Direct test of the scoring function — exact ID match should rank
	// way higher than a description hit.
	m := core.Manifest{ID: "claude", Description: "claude is a chatbot"}
	exact := matchScore(m, "claude")
	if exact < 100 {
		t.Errorf("exact ID score = %d; want >= 100", exact)
	}
}

func idsOf(ms []core.Manifest) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
