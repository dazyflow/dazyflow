// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// escapePathSeg URL-escapes one path segment so a shipment id containing
// reserved characters (or a hostile "../") can't reshape the request path.
func escapePathSeg(s string) string { return url.PathEscape(s) }

// stringField reads a top-level field from a decoded ExtAPI object as a string,
// accepting the JSON string and number spellings nShift uses for ids (an id may
// come back as "774" or 774 depending on the resource). Returns "" when absent.
func stringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
		// Ids are whole numbers; render without a trailing ".0".
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	}
	return ""
}

// trackingNumbers pulls the parcel tracking numbers out of a created/fetched
// shipment. The ExtAPI carries them under shipment.parcels[].copyNo /
// .parcelNo (the label/tracking number). Missing or oddly-shaped data yields an
// empty slice — the full shipment JSON is always on the 'shipment' pin, so this
// is a convenience extraction, never the source of truth.
func trackingNumbers(m map[string]any) []string {
	parcels, ok := m["parcels"].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, p := range parcels {
		po, ok := p.(map[string]any)
		if !ok {
			continue
		}
		// Prefer the carrier tracking number (copyNo), fall back to parcelNo.
		if n := stringField(po, "copyNo"); n != "" {
			out = append(out, n)
			continue
		}
		if n := stringField(po, "parcelNo"); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// joinTracking renders the parcel tracking numbers for the text output pin.
func joinTracking(nums []string) string { return strings.Join(nums, ", ") }

// jsonObjectInputOr resolves the shipment payload object: a wired 'shipment'
// input port wins over the 'shipment' param. The input may arrive as a decoded
// map (upstream JSON pin) or as raw JSON bytes/string; the param is a decoded
// object from the schema. Returns an empty map when neither is set, and an error
// only when a present value isn't a JSON object (a wiring mistake worth naming).
func jsonObjectInputOr(job core.Job, port string) (map[string]any, error) {
	if in, ok := job.Input[port]; ok && in.Inline != nil {
		switch v := in.Inline.(type) {
		case map[string]any:
			return v, nil
		case string:
			return decodeObject([]byte(v), port)
		case []byte:
			return decodeObject(v, port)
		default:
			return nil, fmt.Errorf("'%s' input must be a JSON object", port)
		}
	}
	if v, ok := job.Params[port]; ok && v != nil {
		if m, ok := v.(map[string]any); ok {
			return m, nil
		}
		return nil, fmt.Errorf("'%s' must be a JSON object", port)
	}
	return nil, nil
}

// decodeObject parses raw JSON that must be an object, naming the port on
// failure so the wiring mistake is obvious in the run viewer.
func decodeObject(raw []byte, port string) (map[string]any, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("'%s' input must be a JSON object", port)
	}
	return m, nil
}

// firstShipment normalises a /shipments response into (extractable object, full
// decoded value for the pin). A create can return a single shipment object or a
// one-element array of them; either way we surface the first object for the
// convenience pins and pass the whole decoded response through unchanged.
func firstShipment(body []byte) (map[string]any, any) {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil
	}
	switch t := v.(type) {
	case map[string]any:
		return t, t
	case []any:
		if len(t) > 0 {
			if m, ok := t[0].(map[string]any); ok {
				return m, v
			}
		}
	}
	return nil, v
}
