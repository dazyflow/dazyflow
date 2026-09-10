// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:       "format_text",
			Version:  "1.0",
			Label:    "Change text",
			Subtitle: "Upper case, trim, replace…",
			Icon:     "type",
			Category: "transformation",
			Provider: "internal",
			Tags: []string{"text", "format", "uppercase", "lowercase", "trim", "truncate",
				"replace", "split", "capitalise", "capitalize", "clean", "tidy", "string"},
			Description: "The small text jobs, without writing a formula. Pick what to do and the text comes out the " +
				"other side:\n\n" +
				"UPPER CASE, lower case, Title Case, and Sentence case — the last two lower the rest of the word or " +
				"line first, so a name typed in capitals comes out readable.\n\n" +
				"Tidy up removes the spaces at both ends and collapses runs of spaces, tabs and newlines in the middle " +
				"into one — the fix for text pasted out of a web page. Shorten cuts to a number of characters and adds " +
				"an ellipsis only if it actually cut. Use this instead swaps in your text when what arrived was empty.\n\n" +
				"Find and replace swaps every occurrence of one piece of text for another, plainly — no pattern " +
				"language, so a full stop means a full stop. Split cuts the text on a separator and gives you the " +
				"pieces as a list, and as rows, so \"a, b, c\" can become three things to loop over.\n\n" +
				"For anything with a pattern in it, use the Regex step; for arithmetic on a value, the Expression step.",
			Summary: "Change a piece of text: case, tidy up, shorten, default, find and replace, or split into a list.",
			Examples: []core.ParamsExample{
				{
					Title:  "Tidy up text pasted from a page",
					Params: json.RawMessage(`{"op":"tidy"}`),
					Notes:  "Trims both ends and collapses the newlines and double spaces in the middle.",
				},
				{
					Title:  "A name typed in capitals, made readable",
					Params: json.RawMessage(`{"op":"title"}`),
					Notes:  "\"ADA LOVELACE\" comes out \"Ada Lovelace\".",
				},
				{
					Title:  "Fill in a blank",
					Params: json.RawMessage(`{"op":"default","value":"(no subject)"}`),
					Notes:  "Only when the text is empty or all spaces; anything else passes through.",
				},
				{
					Title:  "Shorten for a notification",
					Params: json.RawMessage(`{"op":"shorten","length":140}`),
				},
				{
					Title:  "Split a list someone typed",
					Params: json.RawMessage(`{"op":"split","separator":","}`),
					Notes:  "\"a, b, c\" becomes three pieces, spaces trimmed — on 'Result' as a list, and on 'Pieces' as rows to loop over.",
				},
				{
					Title:  "Find and replace",
					Params: json.RawMessage(`{"op":"replace","find":"Ltd.","replacement":"Limited"}`),
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "Text", Required: true, MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "out", Label: "Result"},
				{Port: "rows", Label: "Pieces", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"op":{"type":"string","title":"What to do","enum":["upper","lower","title","sentence","tidy","shorten","default","replace","split"],"enumNames":["UPPER CASE","lower case","Title Case","Sentence case","Tidy up the spaces","Shorten it","Use this instead, when empty","Find and replace","Split into pieces"],"description":"The one change to make. For pattern matching use the Regex step instead."},
					"length":{"type":"integer","minimum":1,"default":100,"title":"How many characters","x_visible_when":{"op":"shorten"},"description":"Cut to this many characters. An ellipsis is added only when something was actually cut."},
					"value":{"type":"string","title":"Use this instead","x_visible_when":{"op":"default"},"description":"What to send out when the text is empty or nothing but spaces."},
					"find":{"type":"string","title":"Find","x_visible_when":{"op":"replace"},"description":"The text to look for, taken literally — a full stop means a full stop."},
					"replacement":{"type":"string","title":"Replace with","x_visible_when":{"op":"replace"},"description":"What to put in its place. Leave empty to remove what you found."},
					"separator":{"type":"string","default":",","title":"Split on","x_visible_when":{"op":"split"},"description":"The separator to cut on — a comma, a semicolon, \" | \". Use \\n for one piece per line. Spaces around each piece are trimmed."}
				},
				"required":["op"]
			}`),
			Idempotent: true,
		},
		Execute: executeFormatText,
	})
}

func executeFormatText(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	op := strings.ToLower(strings.TrimSpace(params.StringDefault(job.Params, "op", "")))
	if op == "" {
		return params.Err(job, "bad_param", "'What to do' is required"), nil
	}
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required — wire in the text to change"), nil
	}
	text, ok := textOf(ref.Inline)
	if !ok {
		return params.Err(job, "bad_input", fmt.Sprintf("expected text, got %T", ref.Inline)), nil
	}

	switch op {
	case "upper":
		return textResult(job, strings.ToUpper(text)), nil
	case "lower":
		return textResult(job, strings.ToLower(text)), nil
	case "title":
		// Lower first: a name shouted in capitals is the case this is for, and
		// Title alone would leave "ADA" as "ADA".
		return textResult(job, cases.Title(language.Und).String(strings.ToLower(text))), nil
	case "sentence":
		return textResult(job, sentenceCase(text)), nil
	case "tidy":
		return textResult(job, squish(text)), nil
	case "shorten":
		return textResult(job, shorten(text, params.ClampInt(params.IntDefault(job.Params, "length", 100), 1, 1_000_000))), nil
	case "default":
		if strings.TrimSpace(text) == "" {
			return textResult(job, params.StringDefault(job.Params, "value", "")), nil
		}
		return textResult(job, text), nil
	case "replace":
		find := params.StringDefault(job.Params, "find", "")
		if find == "" {
			return params.Err(job, "bad_param", "'Find' is required — say what to look for"), nil
		}
		return textResult(job, strings.ReplaceAll(text, find, params.StringDefault(job.Params, "replacement", ""))), nil
	case "split":
		return splitResult(job, text, params.StringDefault(job.Params, "separator", ",")), nil
	}
	return params.Err(job, "bad_param", fmt.Sprintf("unknown 'What to do' value %q", op)), nil
}

func textOf(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case []byte:
		return string(t), true
	case nil:
		return "", true
	case float64, int, int64, bool, json.Number:
		return fmt.Sprint(t), true
	}
	return "", false
}

func textResult(job core.Job, s string) core.Result {
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{"out": {MIME: "text/plain", Inline: s}},
	}
}

// splitResult emits the pieces twice over: as a list for anything that wants
// one value, and as rows so the pieces can be looped over or written to a
// sheet without a step in between.
func splitResult(job core.Job, text, separator string) core.Result {
	if separator == "" {
		separator = ","
	}
	separator = strings.ReplaceAll(separator, "\\n", "\n")
	separator = strings.ReplaceAll(separator, "\\t", "\t")

	parts := strings.Split(text, separator)
	pieces := make([]any, 0, len(parts))
	rows := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		pieces = append(pieces, p)
		rows = append(rows, map[string]any{"value": p})
	}
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"out":  {MIME: "application/json", Inline: pieces},
			"rows": {MIME: "application/json", Inline: rows, Headers: []string{"value"}},
		},
	}
}

func sentenceCase(s string) string {
	lowered := strings.ToLower(s)
	for i, r := range lowered {
		if strings.ContainsRune(" \t\n\r", r) {
			continue
		}
		return lowered[:i] + strings.ToUpper(string(r)) + lowered[i+utf8.RuneLen(r):]
	}
	return lowered
}

// shorten cuts on rune boundaries, so a multi-byte character is never left in
// half, and only marks the cut when there was one.
func shorten(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	count := 0
	for i := range s {
		if count == limit {
			return strings.TrimRight(s[:i], " ") + "…"
		}
		count++
	}
	return s
}
