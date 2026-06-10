package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// paramInt accepts JSON numbers (float64), Go ints, or int64, so a
// `limit:5` param works whether it arrived natively or via JSON.
func paramInt(params map[string]any, key string) (int, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// paramStringArray reads a required array-of-string parameter. JSON
// roundtrips to []any of strings; native callers can pass []string.
func paramStringArray(params map[string]any, key string) ([]string, error) {
	v, ok := params[key]
	if !ok {
		return nil, fmt.Errorf("missing param %q", key)
	}
	return normalizeStringArray(v, key)
}

// normalizeStringArray accepts both []string (native) and []any of
// strings (post-JSON-roundtrip).
func normalizeStringArray(v any, key string) ([]string, error) {
	switch s := v.(type) {
	case []string:
		return s, nil
	case []any:
		out := make([]string, len(s))
		for i, item := range s {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d]: expected string, got %T", key, i, item)
			}
			out[i] = str
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s: expected array of strings, got %T", key, v)
}

// paramStringMap reads a JSON object whose values are all strings —
// used for the column_types parameter.
func paramStringMap(params map[string]any, key string) (map[string]string, bool) {
	v, ok := params[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out, true
}

// isSandboxEscape mirrors the io package's check — kept local so this
// package doesn't import a sibling integration.
func isSandboxEscape(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrInvalid) {
		return true
	}
	msg := err.Error()
	for _, marker := range []string{"path escapes", "outside root", "invalid argument"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// normalizeRows accepts the same shapes as drops/io/excel_write
// — native typed slices in-process, and JSON-roundtripped []any of
// map[string]any when the upstream node went through gRPC or MCP.
func normalizeRows(inline any) ([]map[string]any, error) {
	if inline == nil {
		return nil, nil
	}
	switch v := inline.(type) {
	case []map[string]any:
		return v, nil
	case []map[string]string:
		out := make([]map[string]any, len(v))
		for i, r := range v {
			m := make(map[string]any, len(r))
			for k, val := range r {
				m[k] = val
			}
			out[i] = m
		}
		return out, nil
	case []any:
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, err := coerceRowMap(item)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	case string:
		// An empty string is "no rows" rather than malformed JSON. This
		// shows up when a webhook trigger fires with no request body
		// (buildWebhookSeed defaults body to "") and the graph wires
		// webhook_input.body straight into a store's rows port — a
		// common shape for hosted-form flows. Returning a nil slice
		// here keeps the empty-payload path quiet; the caller's
		// "len(rows) == 0 → insert nothing" branch handles the rest.
		if v == "" {
			return nil, nil
		}
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("rows: unsupported input type %T", inline)
}

func coerceRowMap(item any) (map[string]any, error) {
	switch m := item.(type) {
	case map[string]any:
		return m, nil
	case map[string]string:
		out := make(map[string]any, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out, nil
	}
	return nil, fmt.Errorf("expected object, got %T", item)
}

func normalizeHeaders(inline any) ([]string, error) {
	switch v := inline.(type) {
	case []string:
		return v, nil
	case []any:
		out := make([]string, len(v))
		for i, h := range v {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("headers[%d]: expected string, got %T", i, h)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("headers: unsupported input type %T", inline)
}

// deriveHeaders gives a stable column ordering when the user didn't
// wire a "headers" input — the union of row keys, sorted alphabetically.
func deriveHeaders(rows []map[string]any) []string {
	seen := map[string]struct{}{}
	for _, r := range rows {
		for k := range r {
			seen[k] = struct{}{}
		}
	}
	headers := make([]string, 0, len(seen))
	for k := range seen {
		headers = append(headers, k)
	}
	sort.Strings(headers)
	return headers
}
