// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"git.sr.ht/~klahr/dazyflow/drops/internal/rows"
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

// parseColumnTypes reads the optional column_types parameter and
// validates every value, since these strings are spliced directly
// into DDL (see validateColumnType). It is the single boundary all
// db drops go through, so no ensure/evolve function can splice an
// unvalidated type. Returns a nil map when the parameter is absent.
func parseColumnTypes(params map[string]any) (map[string]string, error) {
	m, ok := paramStringMap(params, "column_types")
	if !ok {
		return nil, nil
	}
	for col, t := range m {
		if err := validateColumnType(t); err != nil {
			return nil, fmt.Errorf("column_types[%q]: %w", col, err)
		}
	}
	return m, nil
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

// normalizeRows / coerceRowMap / normalizeHeaders / deriveHeaders are
// thin aliases over the shared drops/internal/rows package. The db
// drops only accept list shapes (a bare object is rejected) and do not
// pre-cap the input here, so they pass the zero-value Options.
func normalizeRows(inline any) ([]map[string]any, error) {
	return rows.Normalize(inline, rows.Options{})
}

func coerceRowMap(item any) (map[string]any, error) {
	return rows.CoerceRowMap(item)
}

func normalizeHeaders(inline any) ([]string, error) {
	return rows.NormalizeHeaders(inline)
}

func deriveHeaders(r []map[string]any) []string {
	return rows.DeriveHeaders(r)
}

// subtract returns the elements of a that aren't in b, preserving
// order. Used by the upsert drops to default update_columns to
// (headers \ conflict_columns).
func subtract(a, b []string) []string {
	skip := make(map[string]struct{}, len(b))
	for _, x := range b {
		skip[x] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, x := range a {
		if _, ok := skip[x]; ok {
			continue
		}
		out = append(out, x)
	}
	return out
}
