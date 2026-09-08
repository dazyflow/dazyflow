// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package nshift

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
)

// escapePathSeg URL-escapes one path segment so a shipment id containing
// reserved characters (or a hostile "../") can't reshape the request path.
func escapePathSeg(s string) string { return url.PathEscape(s) }

func stringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case float64:
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

func joinTracking(nums []string) string { return strings.Join(nums, ", ") }

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
