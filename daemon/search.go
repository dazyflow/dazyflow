// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sort"
	"strings"
	"unicode"

	"github.com/dazyflow/dazyflow/core"
)

type DropSearch struct {
	Query string
	// Each filter slice is OR-within (any value matches the manifest's
	// field); the three filter fields are AND-across (categories AND
	// providers AND tags must all pass).
	Categories      []string
	Providers       []string
	Tags            []string
	IncludeDisabled bool
}

func searchManifests(manifests map[string]core.Manifest, q DropSearch) []core.Manifest {
	type scored struct {
		m     core.Manifest
		score int
	}
	out := make([]scored, 0, len(manifests))
	phrase, words := queryTerms(q.Query)

	for _, m := range manifests {
		if !filtersPass(m, q) {
			continue
		}
		score := 0
		if phrase != "" {
			score = scoreManifest(m, phrase, words)
			if score == 0 {
				continue
			}
		}
		out = append(out, scored{m: m, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if phrase != "" && out[i].score != out[j].score {
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

func filtersPass(m core.Manifest, q DropSearch) bool {
	if len(q.Categories) > 0 && !slicesContainsIgnoreCase(q.Categories, m.Category) {
		return false
	}
	if len(q.Providers) > 0 && !slicesContainsIgnoreCase(q.Providers, m.Provider) {
		return false
	}
	if len(q.Tags) > 0 {
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
	// Prose matches on the WHOLE word. As a substring, "form" hit "format"
	// and "transform", which is how a search for it ranked build_csv and
	// dedupe_rows above form_input.
	if containsWord(strings.ToLower(m.Summary), needle) {
		score += 30
	}
	if containsWord(desc, needle) {
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

// queryTerms splits a search query into the phrase and the words worth scoring
// on their own. Words under three characters are dropped: "to" and "my" match
// half the catalogue and say nothing about intent.
func queryTerms(query string) (phrase string, words []string) {
	phrase = strings.ToLower(strings.TrimSpace(query))
	if phrase == "" {
		return "", nil
	}
	fields := strings.Fields(phrase)
	if len(fields) < 2 {
		return phrase, nil
	}
	for _, f := range fields {
		if len([]rune(f)) >= 3 {
			words = append(words, f)
		}
	}
	return phrase, words
}

// scoreManifest ranks a manifest against a query. The whole phrase scores
// first, so an exact id or label still wins outright; then each word scores on
// its own and a coverage bonus rewards a manifest accounting for more of the
// query.
//
// Any word may match, rather than all of them. Requiring every word is what
// made multi-word queries collapse: "web form" found only http_upload and
// "public form" found nothing at all, though form_input carries both words
// between its id and its tags.
func scoreManifest(m core.Manifest, phrase string, words []string) int {
	score := matchScore(m, phrase)
	if len(words) == 0 {
		return score
	}
	hits := 0
	for _, w := range words {
		if s := matchScore(m, w); s > 0 {
			score += s
			hits++
		}
	}
	if hits > 0 {
		score += hits * 50
	}
	return score
}

// rankManifests returns the manifests matching query, most relevant first and
// alphabetical within a tie so the order is stable across calls.
func rankManifests(mans []core.Manifest, query string) []core.Manifest {
	phrase, words := queryTerms(query)
	if phrase == "" {
		return nil
	}
	type scored struct {
		m     core.Manifest
		score int
	}
	hits := make([]scored, 0, len(mans))
	for _, m := range mans {
		if s := scoreManifest(m, phrase, words); s > 0 {
			hits = append(hits, scored{m: m, score: s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].m.ID < hits[j].m.ID
	})
	out := make([]core.Manifest, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out
}

// containsWord reports whether word appears in text delimited by something
// other than a letter or digit, so "form" does not match "format".
func containsWord(text, word string) bool {
	if word == "" || text == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], word)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(word)
		if !alphanumAt(text, start-1) && !alphanumAt(text, end) {
			return true
		}
		i = start + 1
		if i >= len(text) {
			return false
		}
	}
}

func alphanumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	r := rune(s[i])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func slicesContainsIgnoreCase(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}
