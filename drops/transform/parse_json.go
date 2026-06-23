package transform

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "parse_json",
			Version:     "1.0",
			Label:       "Parse JSON",
			Subtitle:    "Read fields from text",
			Icon:        "braces",
			Category:    "transformation",
			Provider:    "internal",
			Tags:        []string{"transform", "json", "parse", "rows", "etl"},
			Description: "Turn JSON text into rows. Feed it the text output of an AI step or an HTTP response and it parses the JSON into the standard rows + headers shape that Sheets, Excel, Postgres, and the transform family consume. A JSON array of objects becomes one row each; a single object becomes one row. Tolerates the wrappers models add: leading/trailing prose and Markdown code fences (```json … ```) are stripped before parsing. Use 'path' to reach an array nested inside an envelope (e.g. \"data.items\").",
			Summary:     "Parse JSON text (incl. fenced AI output) into rows + headers for downstream tabular drops.",
			Examples: []core.ParamsExample{
				{
					Title:  "Parse an AI step's JSON array straight into rows",
					Params: json.RawMessage(`{}`),
					Notes:  "Wire the AI 'text' output into 'in'. Code fences and surrounding prose are stripped automatically.",
				},
				{
					Title:  "Pull the array out of an API envelope",
					Params: json.RawMessage(`{"path":"data.results"}`),
					Notes:  "When the JSON is {\"data\":{\"results\":[…]}}, path digs to the array before turning it into rows.",
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
					"path":  {"type":"string","description":"Optional dot-path into the parsed JSON before rows are built, e.g. \"data.items\". Each segment indexes an object key."},
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
		return errResult(job, "missing_input", "input port 'in' is required"), nil
	}

	value, err := parseJSONInput(ref.Inline, job.Params)
	if err != nil {
		return errResult(job, "bad_input", err.Error()), nil
	}

	if pathRaw, ok := job.Params["path"].(string); ok && pathRaw != "" {
		value, err = digPath(value, pathRaw)
		if err != nil {
			return errResult(job, "bad_param", err.Error()), nil
		}
	}

	rows, err := rowsFromValue(value)
	if err != nil {
		return errResult(job, "not_tabular", err.Error()), nil
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
// and arrays of non-objects are rejected — there's no sensible row shape
// for them (use the 'value' output instead).
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
