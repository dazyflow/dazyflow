// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package daemon

import (
	"sort"
	"strings"
	"unicode"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/internal/svsearch"
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
	all := make([]core.Manifest, 0, len(manifests))
	for _, m := range manifests {
		all = append(all, m)
	}
	plan := planQuery(all, q.Query)

	for _, m := range manifests {
		if !filtersPass(m, q) {
			continue
		}
		score := 0
		if plan.phrase != "" {
			score = plan.score(m)
			if score == 0 {
				continue
			}
		}
		out = append(out, scored{m: m, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if plan.phrase != "" && out[i].score != out[j].score {
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
	score += tagScore(m, needle)
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
		// A one-word search is a deliberate one: score it whole, stop-words
		// and all, so searching "not" or "text" still finds those steps.
		return phrase, nil
	}
	for _, f := range fields {
		f = strings.Trim(f, ".,:;!?\"'()")
		if len([]rune(f)) < 2 || stopWords[f] {
			continue
		}
		words = append(words, f)
	}
	return phrase, words
}

// stopWords are the words an ask is made of rather than about. They were doing
// real damage as search terms: "the" is a substring of "weather", so it dragged
// weather_current and weather_forecast into eight unrelated asks, and "run"
// pulled in run_on_runner. Only multi-word queries consult this list.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "without": true,
	"from": true, "into": true, "onto": true, "out": true, "over": true,
	"under": true, "about": true, "are": true, "was": true, "were": true,
	"been": true, "being": true, "does": true, "did": true, "done": true,
	"have": true, "has": true, "had": true, "will": true, "would": true,
	"can": true, "could": true, "should": true, "shall": true, "may": true,
	"might": true, "must": true, "its": true, "this": true, "that": true,
	"these": true, "those": true, "they": true, "them": true, "their": true,
	"our": true, "you": true, "your": true, "who": true, "whom": true,
	"whose": true, "which": true, "what": true, "when": true, "where": true,
	"why": true, "how": true, "all": true, "any": true, "both": true,
	"each": true, "more": true, "most": true, "other": true, "some": true,
	"such": true, "nor": true, "only": true, "own": true, "same": true,
	"than": true, "too": true, "very": true, "just": true, "now": true,
	"also": true, "get": true, "got": true, "put": true, "make": true,
	"made": true, "want": true, "need": true, "please": true, "one": true,
	"two": true, "back": true, "again": true, "still": true, "run": true,
	"something": true, "someone": true, "somebody": true, "anyone": true,
	"anybody": true, "everyone": true, "everybody": true, "there": true,
	"here": true, "then": true, "else": true, "let": true, "like": true,
	"new": true, "old": true, "next": true, "last": true,
	// Two-letter words, live now that the length floor is 2.
	"to": true, "of": true, "in": true, "on": true, "at": true, "by": true,
	"is": true, "it": true, "as": true, "be": true, "do": true, "if": true,
	"or": true, "an": true, "my": true, "me": true, "we": true, "us": true,
	"so": true, "up": true, "am": true, "no": true, "he": true, "id": true,
	// Swedish function words. The UI palette translates a Swedish query through
	// its own alias table (web/src/lib/dropSearch.ts); this side does not, so at
	// minimum the words that carry no intent must not score.
	"och": true, "att": true, "det": true, "den": true, "som": true,
	"för": true, "till": true, "från": true, "med": true, "utan": true,
	"min": true, "mitt": true, "mina": true, "mig": true, "jag": true,
	"vi": true, "oss": true, "var": true, "vad": true, "när": true,
	"hur": true, "vem": true, "vilka": true, "vilken": true, "vilket": true,
	"ett": true, "en": true, "kan": true, "ska": true, "skall": true,
	"har": true, "hade": true, "blir": true, "gör": true, "göra": true,
	"kör": true, "vill": true, "behöver": true, "sedan": true, "eller": true,
	"inte": true, "också": true, "bara": true, "alla": true, "varje": true,
	"någon": true, "nagon": true, "något": true, "nagot": true, "några": true,
	"där": true, "här": true, "sätt": true, "gång": true, "igen": true,
	"nytt": true, "nya": true, "ny": true, "ligger": true, "blev": true,
	"åt": true, "om": true, "av": true, "ur": true,
}

// queryPlan is a query prepared against a particular catalogue: the phrase, the
// words worth scoring, and the alias terms for the words the catalogue cannot
// answer literally.
type queryPlan struct {
	phrase string
	words  []string
	alias  map[string][]string
}

// planQuery decides which words need the Swedish table, ONCE for the whole
// query rather than once per step. Deciding it per step meant a word the
// catalogue answers perfectly well still expanded for every step that happened
// to lack it: "summarise" is answered by the summarise steps' own tag, yet it
// also reached the Swedish "summa" and put group_aggregate first.
func planQuery(mans []core.Manifest, query string) queryPlan {
	p := queryPlan{}
	p.phrase, p.words = queryTerms(query)
	if p.phrase == "" {
		return p
	}
	tokens := p.words
	if len(tokens) == 0 && !strings.Contains(p.phrase, " ") {
		tokens = []string{p.phrase}
	}
	for _, tok := range tokens {
		if answeredLiterally(mans, tok) {
			continue
		}
		if terms := svsearch.Expand(tok); len(terms) > 0 {
			if p.alias == nil {
				p.alias = map[string][]string{}
			}
			p.alias[tok] = terms
		}
	}
	return p
}

// answeredLiterally reports whether any step in the catalogue matches the token
// as written. A token that does needs no translating.
func answeredLiterally(mans []core.Manifest, tok string) bool {
	for i := range mans {
		if termScore(mans[i], tok) > 0 {
			return true
		}
	}
	return false
}

// score ranks one step. The whole phrase scores first, so an exact id or label
// still wins outright; then each word scores on its own and a coverage bonus
// rewards a step accounting for more of the query.
//
// One word is a deliberate search, so it also goes through the Swedish table:
// "kalkylark" reaches the Sheets steps on its own. A MULTI-word query must not,
// even when every word turned out to be filler — "för för" folds to "forfor",
// the ending stripper cuts "or", and the remaining "forf" prefix-matches
// "förfrågan", which is how two filler words scored thirty-one steps.
func (p queryPlan) score(m core.Manifest) int {
	if p.phrase == "" {
		return 0
	}
	if !strings.Contains(p.phrase, " ") {
		if s := matchScore(m, p.phrase); s > 0 {
			return s
		}
		return p.aliasScore(m, p.phrase, matchScore)
	}
	score := matchScore(m, p.phrase)
	hits := 0
	for _, w := range p.words {
		s := termScore(m, w)
		if s == 0 {
			s = p.aliasScore(m, w, termScore)
		}
		if s > 0 {
			score += s
			hits++
		}
	}
	if hits > 0 {
		score += hits * 50
	}
	return score
}

// aliasScore is the best score any English reading of a Swedish token earns,
// held below the literal hit it stands in for (svsearch.AliasWeight).
//
// Consulted ONLY for a token that matched nothing literally, which is what makes
// "adding Swedish never reorders an English result" true rather than merely
// intended. Weighting alone does not: English words prefix-match Swedish keys,
// so "check" reached "checksumma" and put `hash` in the results for "check every
// five minutes". The web palette keeps that eager expansion on purpose — it
// wants "fakt" to reach "faktura" mid-typing — and nothing is typed here.
//
// Both search paths read one table, so a Swedish word added for the palette also
// works when the same person asks the AI to build the flow.
func (p queryPlan) aliasScore(m core.Manifest, token string, score func(core.Manifest, string) int) int {
	best := 0
	for _, term := range p.alias[token] {
		if s := score(m, term); s > best {
			best = s
		}
	}
	if best == 0 {
		return 0
	}
	return int(float64(best) * svsearch.AliasWeight)
}

// rankManifests returns the manifests matching query, most relevant first and
// alphabetical within a tie so the order is stable across calls.
func rankManifests(mans []core.Manifest, query string) []core.Manifest {
	plan := planQuery(mans, query)
	if plan.phrase == "" {
		return nil
	}
	type scored struct {
		m     core.Manifest
		score int
	}
	hits := make([]scored, 0, len(mans))
	for _, m := range mans {
		if s := plan.score(m); s > 0 {
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

// tagScore accumulates across every matching tag rather than stopping at the
// first. Matching both "google" and "calendar" says more about gcal_create_event
// than matching either alone, and the first-match-only loop could not say it.
// Capped so a tag-stuffed manifest cannot outrank a real name match.
func tagScore(m core.Manifest, needle string) int {
	score := 0
	for _, tag := range m.Tags {
		t := strings.ToLower(tag)
		switch {
		case t == needle:
			score += 55
		case containsWord(t, needle):
			score += 20
		case stemMatch(t, needle):
			// "week" must reach the "weekly" tag, and a tag "arrive" must
			// answer someone who typed "arrives".
			score += 12
		}
	}
	if score > 90 {
		score = 90
	}
	return score
}

// termScore ranks a manifest against ONE word of a multi-word ask. It withholds
// the exact-id and exact-label jackpots matchScore awards, because those mean
// "the user typed this step's name" — true of a one-word search, false of a
// word that merely appears in a sentence. Without the distinction the `text`
// drop won "text me", `email` won "when a new email arrives", and `number` won
// "pull the invoice number out of the text".
func termScore(m core.Manifest, term string) int {
	id := strings.ToLower(m.ID)
	label := strings.ToLower(m.Label)

	score := 0
	switch {
	case id == term:
		score += 150
	case containsWord(strings.ReplaceAll(id, "_", " "), term):
		// Deliberately close to an exact tag rather than far above it: a tag is
		// someone stating what the step is for, while a word inside an id is
		// often incidental. sort_rows contains "rows" and discord_send_message
		// contains "message", and at 120 each outranked render_text — which
		// carries "message" as a tag — for "turn the rows into a message".
		score += 70
	case strings.HasPrefix(id, term):
		score += 60
	case len(term) >= 5 && strings.Contains(id, term):
		// A bare substring of an id, and only for a term long enough that the
		// hit is unlikely to be an accident of spelling.
		score += 40
	}
	if containsWord(label, term) {
		score += 50
	}
	score += tagScore(m, term)
	if containsWord(strings.ToLower(m.Summary), term) {
		score += 30
	}
	if containsWord(strings.ToLower(m.Description), term) {
		score += 10
	}
	if score == 0 {
		return 0
	}
	score += m.SearchBoost
	if score < 1 {
		score = 1
	}
	return score
}

// stemMatch reports whether one word is the other's prefix, with the shorter at
// least four characters so "for" does not stem to "fortnox".
func stemMatch(a, b string) bool {
	if a == b {
		return true
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if len([]rune(short)) < 4 {
		return false
	}
	return strings.HasPrefix(long, short)
}

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
