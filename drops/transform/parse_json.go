// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "parse_json",
			Version:     "1.0",
			Label:       "Read JSON",
			Subtitle:    "Read fields from text",
			Icon:        "braces",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "json", "parse", "rows", "etl"},
			Description: "Turn JSON text into rows. Feed it the text output of an AI step or an HTTP response and it parses the JSON into the standard rows + headers shape that Sheets, Excel, Postgres, and the transform family consume. A JSON array of objects becomes one row each; a single object becomes one row. Tolerates the wrappers models add: leading/trailing prose and Markdown code fences (```json … ```) are stripped before parsing. Use 'path' to reach an array nested inside an envelope (e.g. \"data.items\"), or to read a single field out — point it at one value and 'Rows' is empty while 'Value' carries it.",
			Summary:     "Parse JSON text (incl. fenced AI output) into rows + headers for the table steps that follow.",
			Examples: []core.ParamsExample{
				{
					Title:  "Parse an AI step's JSON array straight into rows",
					Params: json.RawMessage(`{}`),
					Notes:  "Connect the AI 'text' output into 'in'. Code fences and surrounding prose are stripped automatically.",
				},
				{
					Title:  "Pull the array out of an API envelope",
					Params: json.RawMessage(`{"path":"data.results"}`),
					Notes:  "When the JSON is {\"data\":{\"results\":[…]}}, path digs to the array before turning it into rows.",
				},
				{
					Title:  "Read one field out of a small payload",
					Params: json.RawMessage(`{"path":"version"}`),
					Notes:  "For {\"version\":\"0.27.0\"} this puts \"0.27.0\" on 'Value'. A single value is not a table, so 'Rows' comes out empty — take the answer from 'Value'.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "in", Label: "JSON", Required: true},
			},
			Outputs: []core.Port{
				{Port: "rows", Label: "Rows", MIME: []string{"application/json"}},
				{Port: "value", Label: "Value", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"path":  {"type":"string","description":"Optional dot-path into the parsed JSON before rows are built, e.g. \"data.items\". Each segment indexes an object key. Point it at an object or a list of objects to get rows; point it at a single value (a version string, an id) and 'Rows' comes out empty while 'Value' carries what you asked for."},
					"fence": {"type":"boolean","default":true,"description":"Strip a leading/trailing Markdown code fence and surrounding prose before parsing. On by default so raw LLM output parses cleanly."}
				}
			}`),
			Idempotent: true,
		},
		Execute: executeParseJSON,
	})
}

// executeParseJSON parses the 'in' value into rows. The input ref may
// already be a parsed value (when an upstream drop emitted inline JSON)
// or a raw string (the common case: an AI 'text' output or an HTTP
// response body). Strings go through fence-stripping and json.Unmarshal;
// non-strings are used as-is. After an optional dot-path descent, an
// array becomes one row per element and a lone object becomes a single
// row — matching every other tabular drop's rows + headers contract.
func executeParseJSON(_ context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	ref, ok := job.Input["in"]
	if !ok {
		return params.Err(job, "missing_input", "input port 'in' is required"), nil
	}

	value, err := parseJSONInput(ref.Inline, job.Params)
	if err != nil {
		return params.Err(job, "bad_input", err.Error()), nil
	}

	pathRaw, _ := job.Params["path"].(string)
	dug := pathRaw != ""
	if dug {
		value, err = digPath(value, pathRaw)
		if err != nil {
			return params.Err(job, "bad_param", err.Error()), nil
		}
	}

	rows, err := rowsFromValue(value)
	if err != nil {
		// A path is an explicit instruction to dig to one place, and what sits
		// there is very often a scalar — a version string, an id, a count. To
		// fail the whole step for that is wrong twice over: the caller got
		// exactly what they asked for, and the `value` pin that exists to
		// carry it was unreachable, because rows are built first. So a dug
		// value that isn't row-shaped yields EMPTY rows and a populated value.
		//
		// Only with a path, though. Without one, "the JSON you handed me is
		// not row-shaped" is a genuine mistake — an AI step that returned
		// prose, an API that answered with a bare string — and it still fails
		// rather than handing back zero rows for someone to discover three
		// steps later. Asking for a scalar is a choice; being given one is a
		// surprise.
		if !dug {
			return params.Err(job, "not_tabular", err.Error()), nil
		}
		rows = []map[string]any{}
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"rows":  {MIME: "application/json", Inline: rows, Headers: deriveHeaders(rows)},
			"value": {MIME: "application/json", Inline: value},
		},
	}, nil
}

// parseJSONInput returns the parsed JSON value for the input ref. A
// string is fence-stripped (unless fence=false) and unmarshalled; any
// other inline value is already structured and passes through.
func parseJSONInput(inline any, params map[string]any) (any, error) {
	if inline == nil {
		return nil, fmt.Errorf("input 'in' is empty")
	}
	s, isString := inline.(string)
	if !isString {
		// Upstream already handed us a parsed value (object, array, …).
		return inline, nil
	}

	fence := true
	if f, ok := params["fence"].(bool); ok {
		fence = f
	}
	if fence {
		s = stripFence(s)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("input 'in' is empty after trimming")
	}

	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, fmt.Errorf("input is not valid JSON: %w", err)
	}
	return v, nil
}

// stripFence pulls JSON out of the wrappers language models add. It
// prefers the contents of the first Markdown code fence (```json … ```
// or a bare ``` … ```); failing that it falls back to the substring
// between the first opening and last closing bracket of a matching
// pair, so leading/trailing prose ("Here is the data: […]") is dropped.
func stripFence(s string) string {
	if _, rest, found := strings.Cut(s, "```"); found {
		// Drop an optional language tag on the fence's opening line.
		if firstLine, afterNL, ok := strings.Cut(rest, "\n"); ok {
			if t := strings.TrimSpace(firstLine); t == "" || isFenceLang(t) {
				rest = afterNL
			}
		}
		if body, _, ok := strings.Cut(rest, "```"); ok {
			return strings.TrimSpace(body)
		}
		return strings.TrimSpace(rest)
	}
	return bracketSpan(s)
}

// isFenceLang reports whether a code-fence info string is a bare
// language tag (letters/digits, no spaces) rather than the first line
// of JSON. "json", "JSON5" → true; "{" or "[{" → false.
func isFenceLang(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return len(s) > 0
}

// bracketSpan returns the substring spanning the outermost JSON array
// or object in s, dropping any surrounding prose. It picks whichever of
// '[' or '{' appears first and matches it to the last corresponding
// closing bracket. If no pair is found, s is returned unchanged.
func bracketSpan(s string) string {
	open := strings.IndexAny(s, "[{")
	if open < 0 {
		return s
	}
	var closeCh byte
	if s[open] == '[' {
		closeCh = ']'
	} else {
		closeCh = '}'
	}
	closeIdx := strings.LastIndexByte(s, closeCh)
	if closeIdx <= open {
		return s
	}
	return strings.TrimSpace(s[open : closeIdx+1])
}

// digPath descends dot-separated object keys into a parsed value.
func digPath(v any, path string) (any, error) {
	for seg := range strings.SplitSeq(path, ".") {
		if seg == "" {
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path segment %q: value is %T, not an object", seg, v)
		}
		next, ok := obj[seg]
		if !ok {
			return nil, fmt.Errorf("path segment %q not found", seg)
		}
		v = next
	}
	return v, nil
}

// rowsFromValue turns a parsed JSON value into rows: an array becomes
// one row per element, a single object becomes a one-row table. Scalars
// and arrays of non-objects have no sensible row shape, so they error
// here — the caller decides what that means. With an explicit `path` it
// is not a failure but an answer, and executeParseJSON serves it on the
// 'value' output with empty rows; without one it fails the step.
func rowsFromValue(v any) ([]map[string]any, error) {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for i, item := range t {
			m, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, not an object; JSON arrays must hold objects to become rows", i, item)
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		return []map[string]any{t}, nil
	case nil:
		return nil, fmt.Errorf("parsed value is null")
	default:
		return nil, fmt.Errorf("parsed value is %T, expected a JSON object or array of objects", v)
	}
}
