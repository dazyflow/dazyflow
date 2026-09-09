// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rows holds the row/header normalization shared by the database
// connectors (drops/db) and the data-shaping drops (drops/transform). Both
// consume the same external shapes — native typed slices in-process, and the
// []any / map[string]any forms that arrive once a payload has round-tripped
// through JSON, gRPC or MCP — so the coercion lives here once.
//
// The two callers differ in two ways, both expressed through Options on
// Normalize: drops/transform caps the input against the per-drop row ceiling and
// accepts a single object as a one-row list; drops/db does neither.
package rows

import (
	"encoding/json"
	"fmt"
	"sort"
)

type Options struct {
	// Cap, when non-nil, is called with the candidate row count before
	// the list is materialized; a non-nil error from it aborts the
	// normalization. drops/transform passes a limits.MaxRows check here
	// so an oversized input is refused rather than first allocated.
	Cap               func(n int) error
	AllowSingleObject bool
}

func (o Options) cap(n int) error {
	if o.Cap == nil {
		return nil
	}
	return o.Cap(n)
}

func Normalize(inline any, opt Options) ([]map[string]any, error) {
	if inline == nil {
		return nil, nil
	}
	switch v := inline.(type) {
	case []map[string]any:
		if err := opt.cap(len(v)); err != nil {
			return nil, err
		}
		return v, nil
	case []map[string]string:
		if err := opt.cap(len(v)); err != nil {
			return nil, err
		}
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
		if err := opt.cap(len(v)); err != nil {
			return nil, err
		}
		out := make([]map[string]any, 0, len(v))
		for i, item := range v {
			m, err := CoerceRowMap(item)
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", i, err)
			}
			out = append(out, m)
		}
		return out, nil
	case map[string]any:
		if !opt.AllowSingleObject {
			break
		}
		return []map[string]any{v}, nil
	case map[string]string:
		if !opt.AllowSingleObject {
			break
		}
		m := make(map[string]any, len(v))
		for k, val := range v {
			m[k] = val
		}
		return []map[string]any{m}, nil
	case string:
		if v == "" {
			return nil, nil
		}
		if opt.AllowSingleObject {
			var parsed any
			if err := json.Unmarshal([]byte(v), &parsed); err != nil {
				return nil, fmt.Errorf("rows JSON: %w", err)
			}
			return Normalize(parsed, opt)
		}
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return nil, fmt.Errorf("rows JSON: %w", err)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("rows: unsupported input type %T", inline)
}

func CoerceRowMap(item any) (map[string]any, error) {
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

func DeriveHeaders(rows []map[string]any) []string {
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

func Cell(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
