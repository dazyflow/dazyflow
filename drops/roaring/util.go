// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package roaring

import (
	"net/url"
	"strconv"
	"strings"
)

// escapePathSeg URL-escapes one path segment so an org number containing
// reserved characters (or a hostile "../") can't reshape the request path.
func escapePathSeg(s string) string { return url.PathEscape(s) }

// itoa renders an int for a text output pin.
func itoa(n int) string { return strconv.Itoa(n) }

// country resolves the ISO country segment of a Roaring path, lower-cased and
// defaulting to Sweden — Roaring's data is per-country (se / dk / no / fi).
func country(raw string) string {
	c := strings.ToLower(strings.TrimSpace(raw))
	if c == "" {
		return "se"
	}
	return c
}

// firstString returns the first non-empty string value among the given keys of
// a decoded JSON object, accepting the string and number spellings. Used to pull
// a convenience field (name, status) out of a response whose exact key varies by
// Roaring product/version — the full JSON is always on the record pin, so this
// is best-effort, never the source of truth.
func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

// decodeObject best-effort decodes a response body into a generic object for the
// record pin; a non-object (or parse failure) yields nil, and the drop still
// returns the raw value it was given.
func asObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// countHits reports how many company records a search response carries. Roaring
// search responses vary in the array's key across versions (hits / companies /
// records), so we probe the common names and return the first array's length.
func countHits(m map[string]any) int {
	for _, k := range []string{"hits", "companies", "records", "results"} {
		if arr, ok := m[k].([]any); ok {
			return len(arr)
		}
	}
	return 0
}
