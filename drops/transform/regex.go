// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
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
			Description: "Run a regular expression over text — connect the text in, or type it on the step (so inside a For each you can read ${item.description} with no earlier step). 'mode' picks what to do: extract pulls out every match (with capture groups as columns — the first match also comes out on 'out'), replace substitutes matches (use $1 or ${name} in the replacement), split breaks the text on the pattern into a list, and match tests whether the pattern is found (a boolean for a Branch). Patterns use RE2 syntax; add inline flags like (?i) for case-insensitive. Named groups (?P<name>…) become named columns; unnamed groups become 1, 2, … and the whole match is 'match'.",
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
					"pattern":{"type":"string","title":"Pattern","description":"RE2 regular expression. Inline flags like (?i) work; capture groups become columns in extract mode."},
					"mode":{"type":"string","enum":["extract","replace","split","match"],"default":"extract","title":"Mode","description":"extract matches (rows + first on 'out'), replace (text), split by the pattern (list), or match/test (boolean)."},
					"replacement":{"type":"string","title":"Replacement","description":"For replace mode: the substitution text. $1 / ${name} insert capture groups."},
					"text":{"type":"string","title":"Text to search","format":"multiline","description":"The text to run the pattern over. Or connect the Text input, which overrides this. Inside a For each, type the field to read — e.g. ${item.description}."}
				},
				"required":["pattern"]
			}`),
			Idempotent: true,
		},
		Execute: executeRegex,
	})
}

// executeRegex compiles the pattern and applies it per 'mode'. RE2 (Go's
// regexp) is linear-time, so a pathological pattern can't cause catastrophic
// backtracking — no ReDoS guard is needed beyond compiling once up front.
func executeRegex(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	pattern, ok := job.Params["pattern"].(string)
	if !ok || pattern == "" {
		return errResult(job, "bad_param", "param 'pattern' is required (a regular expression)"), nil
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

	mode := "extract"
	if m, ok := job.Params["mode"].(string); ok && m != "" {
		mode = m
	}

	switch mode {
	case "extract":
		return regexExtract(job, re, text)
	case "replace":
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
