package daemon

import (
	"sort"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// DropSearch describes the filter set ListModules supports. Empty
// fields are wildcards — an empty DropSearch returns everything.
type DropSearch struct {
	// Query substring-matches against ID, Label, Description (case-
	// insensitive). When present it also drives relevance scoring.
	Query string
	// Each filter slice is OR-within (any value matches the manifest's
	// field); the three filter fields are AND-across (categories AND
	// providers AND tags must all pass).
	Categories []string
	Providers  []string
	Tags       []string
}

// searchManifests applies filters + query to a manifest list. Results
// are sorted by relevance when Query is set (highest score first, ties
// broken alphabetically by ID), or by ID alphabetically when Query is
// empty.
func searchManifests(manifests map[string]core.Manifest, q DropSearch) []core.Manifest {
	type scored struct {
		m     core.Manifest
		score int
	}
	out := make([]scored, 0, len(manifests))
	needle := strings.ToLower(q.Query)

	for _, m := range manifests {
		if !filtersPass(m, q) {
			continue
		}
		score := 0
		if needle != "" {
			score = matchScore(m, needle)
			if score == 0 {
				continue
			}
		}
		out = append(out, scored{m: m, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if needle != "" && out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].m.ID < out[j].m.ID
	})

	final := make([]core.Manifest, len(out))
	for i, s := range out {
		final[i] = s.m
	}
	return final
}

// filtersPass checks the category/provider/tag filters. Each filter is
// OR-within (matches any value); across fields the conditions AND.
func filtersPass(m core.Manifest, q DropSearch) bool {
	if len(q.Categories) > 0 && !slicesContainsIgnoreCase(q.Categories, m.Category) {
		return false
	}
	if len(q.Providers) > 0 && !slicesContainsIgnoreCase(q.Providers, m.Provider) {
		return false
	}
	if len(q.Tags) > 0 {
		// A manifest passes the tag filter if any of its tags is in the
		// requested set.
		hit := false
		for _, want := range q.Tags {
			for _, have := range m.Tags {
				if strings.EqualFold(want, have) {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// matchScore returns a relevance number for needle (lower-cased) against
// the manifest. 0 means no match (the manifest is filtered out). Higher
// scores rank earlier.
//
// The scoring is intentionally coarse — we don't have token-level
// search, so the order can be off in the middle. The cases that matter
// (exact ID match, exact label match) dominate.
func matchScore(m core.Manifest, needle string) int {
	id := strings.ToLower(m.ID)
	label := strings.ToLower(m.Label)
	desc := strings.ToLower(m.Description)

	switch {
	case id == needle:
		return 1000
	case label == needle:
		return 500
	case strings.HasPrefix(id, needle):
		return 250
	case strings.HasPrefix(label, needle):
		return 200
	}

	score := 0
	if strings.Contains(id, needle) {
		score += 100
	}
	if strings.Contains(label, needle) {
		score += 50
	}
	if strings.Contains(desc, needle) {
		score += 20
	}
	for _, tag := range m.Tags {
		if strings.EqualFold(tag, needle) {
			score += 40
			break
		}
	}
	for _, tag := range m.Tags {
		if strings.Contains(strings.ToLower(tag), needle) {
			score += 10
			break
		}
	}
	if score == 0 {
		return 0
	}
	// Apply the manifest's own ranking nudge to fuzzy/tag matches only
	// (exact id/label/prefix hits returned above and must stay dominant).
	// Floor at 1 so a negative boost down-ranks without turning a real
	// match into a "no match".
	score += m.SearchBoost
	if score < 1 {
		score = 1
	}
	return score
}

func slicesContainsIgnoreCase(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
