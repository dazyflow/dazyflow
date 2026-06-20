// Package rows holds the row/header normalization shared by the
// database-connector drops (drops/db) and the data-shaping drops
// (drops/transform). Both consume the same external shapes — native
// typed slices in-process, and the []any / map[string]any forms that
// arrive once a payload has round-tripped through JSON, gRPC, or MCP —
// so the coercion logic lives here once instead of being copied per
// integration.
//
// The two callers differ in two small ways, both expressed through
// Options on Normalize:
//
//   - drops/transform caps the input against the per-drop row ceiling
//     (limits.MaxRows) so a transform can't be made to hold an
//     unbounded list; drops/db does not pre-cap here.
//   - drops/transform accepts a single object (a webhook/form JSON
//     body) as a one-row list; drops/db only accepts list shapes.
//
// Everything else — CoerceRowMap, NormalizeHeaders, DeriveHeaders — is
// byte-for-byte identical across the two and exported as-is.
package rows

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Options tunes Normalize for its two callers without changing the
// coercion logic. The zero value is the drops/db behavior: no row cap
// and single objects rejected as an unsupported type.
type Options struct {
	// Cap, when non-nil, is called with the candidate row count before
	// the list is materialized; a non-nil error from it aborts the
	// normalization. drops/transform passes a limits.MaxRows check here
	// so an oversized input is refused rather than first allocated.
	Cap func(n int) error
	// AllowSingleObject makes a lone map (or a JSON object string) parse
	// as a one-row list. This is the shape a webhook or hosted-form
	// trigger emits for a JSON object body, so drops/transform wiring
	// webhook_input.body straight into a rows port just works. drops/db
	// leaves this false and rejects a bare object.
	AllowSingleObject bool
}

func (o Options) cap(n int) error {
	if o.Cap == nil {
		return nil
	}
	return o.Cap(n)
}

// Normalize coerces the supported input shapes into []map[string]any.
//
// nil and the empty string both mean "no rows" (a webhook fired with no
// body), returning a nil slice so the caller's "len(rows)==0 → do
// nothing" branch handles the rest.
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
		// A single object is one row. This is the shape a webhook or
		// hosted-form trigger emits for a JSON object body, so wiring
		// webhook_input.body straight into a transform's rows port — the
		// most common starter shape — just works instead of failing with
		// "unsupported input type".
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
		// An empty string is "no rows" rather than malformed JSON. This
		// shows up when a webhook trigger fires with no request body and
		// the graph wires that body straight into a rows port. Returning
		// a nil slice keeps the empty-payload path quiet.
		if v == "" {
			return nil, nil
		}
		if opt.AllowSingleObject {
			// Parse leniently, accepting either an array of objects or a
			// single object, then re-run normalization on the decoded
			// value so the cap and shape handling apply uniformly.
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

// CoerceRowMap widens a single decoded list element into map[string]any,
// accepting the native map[string]string shape produced by typed
// callers as well as the post-JSON map[string]any.
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

// NormalizeHeaders coerces a headers input ([]string native, or []any of
// strings post-JSON) into []string.
func NormalizeHeaders(inline any) ([]string, error) {
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

// DeriveHeaders gives a stable column ordering when the user didn't wire
// a "headers" input — the union of row keys, sorted alphabetically.
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
