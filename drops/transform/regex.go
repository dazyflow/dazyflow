// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "regex",
			Version:     "1.0",
			Label:       "Regex",
			Subtitle:    "Extract, replace, split, or match",
			Icon:        "regex",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"regex", "regexp", "extract", "replace", "split", "match", "pattern", "text", "transform"},
			Description: "Run a regular expression over text — connect the text in, or type it on the step (so inside a For each you can read ${item.description} with no earlier step). 'mode' picks what to do: extract pulls out every match (with capture groups as columns — the first match also comes out on 'out'), replace substitutes matches (use $1 or ${name} in the replacement, or fill in the 'replacements' table to rewrite several different words in one step), split breaks the text on the pattern into a list, and match tests whether the pattern is found (a boolean for a Branch). Patterns use RE2 syntax; add inline flags like (?i) for case-insensitive. Named groups (?P<name>…) become named columns; unnamed groups become 1, 2, … and the whole match is 'match'.",
			Summary:     "Extract, replace, split, or match text with a regular expression.",
			Examples: []core.ParamsExample{
				{
					Title:  "Extract every number",
					Params: json.RawMessage(`{"pattern":"[0-9]+","mode":"extract"}`),
					Notes:  "One row per match under the 'match' column; first match also on 'out'.",
				},
				{
					Title:  "Pull year & month with named groups",
					Params: json.RawMessage(`{"pattern":"(?P<year>\\d{4})-(?P<month>\\d{2})","mode":"extract"}`),
					Notes:  "Rows get 'year' and 'month' columns (plus the full 'match').",
				},
				{
					Title:  "Collapse whitespace to single dashes",
					Params: json.RawMessage(`{"pattern":"\\s+","mode":"replace","replacement":"-"}`),
				},
				{
					Title:  "Translate several words in one step",
					Params: json.RawMessage(`{"mode":"replace","replacements":{"Clouds":"Molnigt","Rain":"Regn","Snow":"Snö"}}`),
					Notes:  "No pattern needed — the words to look for are the table's own. Each match becomes whatever its row says; anything else is left alone.",
				},
				{
					Title:  "Translate whatever the casing, using your own pattern",
					Params: json.RawMessage(`{"pattern":"(?i)clouds|rain","mode":"replace","replacements":{"Clouds":"Molnigt","Rain":"Regn"}}`),
					Notes:  "The pattern decides what counts as a match, the table decides what it becomes. A match is looked up exactly, then case-insensitively when one row is unambiguous.",
				},
				{
					Title:  "Pull the phone number out of the booking (inside For each)",
					Params: json.RawMessage(`{"pattern":"\\+?[0-9][0-9 ()-]{6,}[0-9]","mode":"extract","text":"${item.description}"}`),
					Notes:  "No earlier step needed — the text is typed on the step, so this works as the first step of a loop body.",
				},
				{
					Title:  "Does it look like an invoice id?",
					Params: json.RawMessage(`{"pattern":"^INV-\\d+$","mode":"match"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				// Not required: the text can equally be typed on the step —
				// which is how it reaches a step inside a For each, as
				// "${item.description}". A wired value wins.
				{Port: "in", Label: "Text", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
				{Port: "rows", Label: "Matches", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"pattern":{"type":"string","title":"Pattern","description":"RE2 regular expression. Inline flags like (?i) work; capture groups become columns in extract mode. Can be left empty in replace mode when a Replacements table is filled in — the words to look for are then the table's own."},
					"mode":{"type":"string","enum":["extract","replace","split","match"],"enumNames":["Extract matches","Replace matches","Split on the pattern","Test for a match"],"default":"extract","title":"Mode","description":"extract matches (rows + first on 'out'), replace (text), split by the pattern (list), or match/test (boolean)."},
					"replacement":{"type":"string","title":"Replacement","description":"For replace mode: the substitution text. $1 / ${name} insert capture groups. Ignored when a Replacements table is filled in."},
					"replacements":{"type":"object","additionalProperties":{"type":"string"},"title":"Replacements","x_key_placeholder":"Clouds","x_value_placeholder":"Molnigt","description":"Replace mode, many words at once: what to look for on the left, what to write on the right. Each match becomes whatever its row says; a match no row mentions is left as it is. Leave Pattern empty and the words are looked for literally, or write a pattern (e.g. with (?i)) and each match is looked up in this table."},
					"text":{"type":"string","title":"Text to search","format":"multiline","description":"The text to run the pattern over. Or connect the Text input, which overrides this. Inside a For each, type the field to read — e.g. ${item.description}."}
				},
				"required":[]
			}`),
			Idempotent: true,
		},
		Execute: executeRegex,
	})
}

// replacementTable reads the `replacements` param: what to look for on the
// left, what to write on the right. Empty when the param is absent.
func replacementTable(params map[string]any) (map[string]string, error) {
	raw, present := params["replacements"]
	if !present || raw == nil {
		return nil, nil
	}
	m, err := normalizeStringMap(raw, "replacements")
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if strings.TrimSpace(k) == "" {
			continue // a half-typed row in the editor
		}
		out[k] = v
	}
	return out, nil
}

// patternFromKeys builds the expression that finds every key in the table:
// each one quoted so punctuation is literal, joined as alternatives.
//
// Longest first, because RE2 alternation is leftmost-first: with "Rain" before
// "Rain shower", the longer phrase would only ever match its first two words
// and the rest of it would be left in the text. Sorting by length removes that
// trap without the author having to know it exists.
func patternFromKeys(table map[string]string) string {
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) > len(keys[j])
		}
		return keys[i] < keys[j] // stable, so the same table always compiles the same
	})
	quoted := make([]string, len(keys))
	for i, k := range keys {
		quoted[i] = regexp.QuoteMeta(k)
	}
	return strings.Join(quoted, "|")
}

// replaceFromTable rewrites each match to whatever the table says that match
// means. A match the table doesn't mention is left exactly as it was — the
// table lists what to change, so anything else is not ours to touch.
//
// Lookup is exact first. Failing that, a case-insensitive match counts when
// exactly ONE key matches that way: an author who writes (?i) in their pattern
// means to catch "CLOUDS" and "clouds" with one row, and it would be strange
// for the pattern to be case-blind while the table wasn't. Two keys differing
// only in case are ambiguous, so neither is guessed at.
func replaceFromTable(re *regexp.Regexp, text string, table map[string]string) string {
	return re.ReplaceAllStringFunc(text, func(match string) string {
		if v, ok := table[match]; ok {
			return v
		}
		var found string
		hits := 0
		for k, v := range table {
			if strings.EqualFold(k, match) {
				found = v
				hits++
			}
		}
		if hits == 1 {
			return found
		}
		return match
	})
}

// executeRegex compiles the pattern and applies it per 'mode'. RE2 (Go's
// regexp) is linear-time, so a pathological pattern can't cause catastrophic
// backtracking — no ReDoS guard is needed beyond compiling once up front.
func executeRegex(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	mode := "extract"
	if m, ok := job.Params["mode"].(string); ok && m != "" {
		mode = m
	}

	pattern, _ := job.Params["pattern"].(string)

	table, err := replacementTable(job.Params)
	if err != nil {
		return errResult(job, "bad_param", err.Error()), nil
	}
	// In replace mode the table doubles as the pattern: with no expression
	// typed, the words to look for ARE its keys, so the everyday
	// find-and-replace needs no regular expression at all.
	//
	// Only in replace mode — the table's values mean nothing to the other three,
	// and core.lintRegexPattern excuses a missing pattern on exactly this
	// condition. A step the linter calls unconfigured must not quietly run.
	if pattern == "" && mode == "replace" && len(table) > 0 {
		pattern = patternFromKeys(table)
	}
	if pattern == "" {
		return errResult(job, "bad_param",
			"param 'pattern' is required (a regular expression) — or, in replace mode, a 'replacements' table to build one from"), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return errResult(job, "bad_param", "invalid regex: "+err.Error()), nil
	}

	text, ok := regexText(job.Input["in"])
	if !ok {
		return errResult(job, "bad_input", "input port 'in' must be text"), nil
	}
	// The wired input wins; the typed param is the fallback — the same shape
	// as the AI steps' Text, and what lets this step read ${item.…} inside a
	// loop body, where there is no upstream node to wire from.
	if text == "" {
		if v, isStr := job.Params["text"].(string); isStr {
			text = v
		}
	}

	switch mode {
	case "extract":
		return regexExtract(job, re, text)
	case "replace":
		if len(table) > 0 {
			return textOut(job, replaceFromTable(re, text, table)), nil
		}
		repl, _ := job.Params["replacement"].(string)
		return textOut(job, re.ReplaceAllString(text, repl)), nil
	case "split":
		parts := re.Split(text, -1)
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"out": {MIME: "application/json", Inline: parts}},
		}, nil
	case "match":
		return core.Result{
			JobID:  job.ID,
			Status: core.StatusOK,
			Output: map[string]core.Ref{"out": {MIME: core.MIMEBool, Inline: re.MatchString(text)}},
		}, nil
	default:
		return errResult(job, "bad_param", fmt.Sprintf("unknown mode %q (use extract, replace, split, or match)", mode)), nil
	}
}

// regexExtract emits one row per match — the whole match under "match" plus a
// column per capture group (named groups by name, unnamed by position) — and
// the first whole match on 'out' for the common "grab one thing" case.
func regexExtract(job core.Job, re *regexp.Regexp, text string) (core.Result, error) {
	matches := re.FindAllStringSubmatch(text, -1)
	if err := capRows(len(matches)); err != nil {
		return errResult(job, "too_large", err.Error()), nil
	}

	names := re.SubexpNames()
	headers := make([]string, 0, len(names))
	headers = append(headers, "match")
	for i := 1; i < len(names); i++ {
		headers = append(headers, groupKey(names, i))
	}

	rows := make([]map[string]any, 0, len(matches))
	for _, m := range matches {
		row := make(map[string]any, len(m))
		row["match"] = m[0]
		for i := 1; i < len(m); i++ {
			row[groupKey(names, i)] = m[i]
		}
		rows = append(rows, row)
	}

	first := ""
	if len(matches) > 0 {
		first = matches[0][0]
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":  {MIME: "text/plain", Inline: first},
			"rows": {MIME: "application/json", Inline: rows, Headers: headers},
		},
	}, nil
}

// groupKey names capture group i: its (?P<name>) name if it has one, else its
// 1-based position as a string.
func groupKey(names []string, i int) string {
	if i < len(names) && names[i] != "" {
		return names[i]
	}
	return strconv.Itoa(i)
}

// regexText reads a text input, accepting a string or raw []byte.
func regexText(ref core.Ref) (string, bool) {
	switch v := ref.Inline.(type) {
	case nil:
		// Unwired (or empty) — not a mistake: the text may be typed on the
		// step instead. The caller falls back to the param.
		return "", true
	case string:
		return v, true
	case []byte:
		return string(v), true
	default:
		return "", false
	}
}

// textOut is the single-text-value OK epilogue shared by the replace path.
func textOut(job core.Job, s string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"out": {MIME: "text/plain", Inline: s}},
	}
}
