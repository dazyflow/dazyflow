// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// upstreamSubstituter draws a string from a completed node's output:
//
//	${upstream.excel_read.headers}        → the whole value, JSON-stringified
//	${upstream.excel_read.headers[0]}     → first element
//	${upstream.postgres_query.rows[0].name}
//
// First segment is the nodeID, second the output port; the rest walk the port's
// Inline value, `.field` into a map and `[N]` into a slice. A mixed-type path
// produces a typed error, so the user sees "expected map, got string at .name"
// rather than an empty result.
//
// Not-ok rather than an error when the scheme isn't "upstream" or prior is nil,
// so a reference in a graph triggered without recorded predecessor outputs
// degrades to the literal placeholder rather than failing the run.
func upstreamSubstituter(prior map[string]core.Result) Substituter {
	return func(_ context.Context, scheme, path string) (string, bool, error) {
		if scheme != "upstream" {
			return "", false, nil
		}
		if prior == nil {
			return "", false, nil
		}
		v, err := resolveUpstreamPath(prior, path)
		if err != nil {
			return "", true, err
		}
		return stringifyForTemplate(v), true, nil
	}
}

func resolveUpstreamPath(prior map[string]core.Result, path string) (any, error) {
	if path == "" {
		return nil, fmt.Errorf("upstream: empty path")
	}
	nodeID, rest, _ := strings.Cut(path, ".")
	if nodeID == "" {
		return nil, fmt.Errorf("upstream: path must start with a node ID")
	}
	result, ok := prior[nodeID]
	if !ok {
		return nil, fmt.Errorf("upstream: no result recorded for node %q", nodeID)
	}
	if rest == "" {
		return nil, fmt.Errorf("upstream: path must include a port (e.g. %q)", nodeID+".out")
	}

	stopAt := strings.IndexAny(rest, ".[")
	var port, tail string
	if stopAt < 0 {
		port = rest
		tail = ""
	} else {
		port = rest[:stopAt]
		tail = rest[stopAt:]
		if tail != "" && tail[0] == '.' {
			tail = tail[1:] // skip the leading dot so walkPath starts on an identifier
		}
	}

	ref, ok := result.Output[port]
	if !ok {
		return nil, fmt.Errorf("upstream: node %q has no output port %q", nodeID, port)
	}
	value := ref.Inline
	if tail == "" {
		return value, nil
	}
	if value == nil {
		return nil, fmt.Errorf("upstream: %s.%s has no inline value to descend into", nodeID, port)
	}
	return walkPath(value, tail)
}

func walkPath(value any, path string) (any, error) {
	pos := 0
	for pos < len(path) {
		switch path[pos] {
		case '.':
			pos++
		case '[':
			end := strings.IndexByte(path[pos+1:], ']')
			if end < 0 {
				return nil, fmt.Errorf("upstream path: unclosed '[' at offset %d", pos)
			}
			idxStr := path[pos+1 : pos+1+end]
			idx, err := strconv.Atoi(idxStr)
			if err != nil {
				return nil, fmt.Errorf("upstream path: bad index %q at offset %d", idxStr, pos)
			}
			value, err = indexValue(value, idx)
			if err != nil {
				return nil, err
			}
			pos += end + 2
		default:
			next := strings.IndexAny(path[pos:], ".[")
			var field string
			if next < 0 {
				field = path[pos:]
				pos = len(path)
			} else {
				field = path[pos : pos+next]
				pos += next
			}
			next2, err := getField(value, field)
			if err != nil {
				return nil, err
			}
			value = next2
		}
	}
	return value, nil
}

func getField(value any, field string) (any, error) {
	switch m := value.(type) {
	case map[string]any:
		if v, ok := m[field]; ok {
			return v, nil
		}
	case map[string]string:
		if v, ok := m[field]; ok {
			return v, nil
		}
	default:
		return nil, fmt.Errorf("upstream path: expected object for field %q, got %T", field, value)
	}
	return nil, fmt.Errorf("upstream path: field %q not present", field)
}

func indexValue(value any, idx int) (any, error) {
	switch s := value.(type) {
	case []any:
		if idx < 0 || idx >= len(s) {
			return nil, fmt.Errorf("upstream path: index %d out of range (len %d)", idx, len(s))
		}
		return s[idx], nil
	case []string:
		if idx < 0 || idx >= len(s) {
			return nil, fmt.Errorf("upstream path: index %d out of range (len %d)", idx, len(s))
		}
		return s[idx], nil
	case []map[string]any:
		if idx < 0 || idx >= len(s) {
			return nil, fmt.Errorf("upstream path: index %d out of range (len %d)", idx, len(s))
		}
		return s[idx], nil
	case []map[string]string:
		if idx < 0 || idx >= len(s) {
			return nil, fmt.Errorf("upstream path: index %d out of range (len %d)", idx, len(s))
		}
		return s[idx], nil
	}
	return nil, fmt.Errorf("upstream path: expected array for [%d], got %T", idx, value)
}

func stringifyForTemplate(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return fmt.Sprint(x)
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprint(v)
}
