// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package db

import (
	"fmt"

	"github.com/dazyflow/dazyflow/drops/internal/rows"
)

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

func paramStringArray(params map[string]any, key string) ([]string, error) {
	v, ok := params[key]
	if !ok {
		return nil, fmt.Errorf("missing param %q", key)
	}
	return normalizeStringArray(v, key)
}

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

func normalizeRows(inline any) ([]map[string]any, error) {
	return rows.Normalize(inline, rows.Options{})
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
