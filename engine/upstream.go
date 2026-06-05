package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/hazyflow/core"
)

// upstreamSubstituter resolves ${upstream.nodeID.port.path...} into a
// string drawn from a previously-completed node's output.
//
// Path syntax:
//
//	${upstream.excel_read.headers}        → entire headers value (JSON-stringified)
//	${upstream.excel_read.headers[0]}     → first element of the headers array
//	${upstream.postgres_query.rows[0].name}
//	${upstream.loader.meta.status}        → a nested map field
//
// First segment is always the nodeID; second is the output port name;
// remaining segments walk the port's Inline value as a tree of
// maps and slices. `.field` descends into a map; `[N]` indexes a
// slice. Mixed-type paths produce a typed error so the user sees
// "expected map, got string at .name" rather than an empty result.
//
// Returns ok=false (not an error) when the scheme isn't "upstream"
// or when prior is nil — that way an `${upstream.…}` reference in a
// graph triggered without recorded predecessor outputs degrades to
// the literal placeholder rather than failing the run.
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

// resolveUpstreamPath drills into prior[nodeID].Output[port].Inline
// using the dot/bracket path syntax described above. Returns the
// raw Go value at that location — stringification happens later.
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

	// The port name is the next segment up to the next '.' OR '[' so
	// that "rows[0]" parses as port=rows then index 0. Trim either
	// terminator before re-parsing the remainder.
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

// walkPath descends through `value` using the path tail. The tail
// uses bare identifiers separated by dots for map lookups and `[N]`
// for slice indexing — same surface as JavaScript object access,
// no escaping, no quoting. Bracket-only access (`rows[0]`) is
// supported by starting the tail with `[`.
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
			// Identifier: read up to the next '.' or '['.
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

// getField reads `field` from a value that should be a map. We
// accept both map[string]any (native) and map[string]string (older
// drops still emit this for excel_read untyped mode).
func getField(value any, field string) (any, error) {
	switch m := value.(type) {
	case map[string]any:
		v, ok := m[field]
		if !ok {
			return nil, fmt.Errorf("upstream path: field %q not present", field)
		}
		return v, nil
	case map[string]string:
		v, ok := m[field]
		if !ok {
			return nil, fmt.Errorf("upstream path: field %q not present", field)
		}
		return v, nil
	}
	return nil, fmt.Errorf("upstream path: expected object for field %q, got %T", field, value)
}

// indexValue reads `idx` from a value that should be a slice. Handles
// the three common slice shapes we actually emit from drops.
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

// stringifyForTemplate renders a resolved value for substitution into
// the surrounding string. Primitive types use Sprint; complex types
// (maps, slices, structs) marshal to JSON so the substitution stays
// machine-readable even when it lands inside another JSON document.
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
