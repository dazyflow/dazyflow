// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package klarna

import (
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
)

// escapePathSeg URL-escapes one path segment so an order id containing reserved
// characters (or a hostile "../") can't reshape the request path.
func escapePathSeg(s string) string { return url.PathEscape(s) }

// itoa64 renders an int64 amount for a text output pin.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

// wholeNumberInputOr returns the whole number wired into input port `port` (a
// JSON number or numeric text), or `fallback` when the port is unwired or empty.
// ok is false when the port carries anything else — including a fractional
// number, which is a wiring mistake (minor-unit amounts are integers), not
// something to silently truncate. Mirrors the Stripe connector's amount reader.
func wholeNumberInputOr(job core.Job, port string, fallback int) (int, bool) {
	in, present := job.Input[port]
	if !present || in.Inline == nil {
		return fallback, true
	}
	fromText := func(s string) (int, bool) {
		s = strings.TrimSpace(s)
		if s == "" {
			return fallback, true
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	switch v := in.Inline.(type) {
	case float64:
		if v != math.Trunc(v) {
			return 0, false
		}
		return int(v), true
	case int:
		return v, true
	case string:
		return fromText(v)
	case []byte:
		return fromText(string(v))
	}
	return 0, false
}

// idFromHeaderOrLocation reads a resource id Klarna returns for a created
// capture/refund: the dedicated header (e.g. "Capture-ID") if present, else the
// last path segment of the Location header ("/…/captures/{id}"). Klarna sends
// both; either alone identifies the new resource.
func idFromHeaderOrLocation(h http.Header, idHeader string) string {
	if h == nil {
		return ""
	}
	if v := strings.TrimSpace(h.Get(idHeader)); v != "" {
		return v
	}
	loc := strings.TrimRight(strings.TrimSpace(h.Get("Location")), "/")
	if i := strings.LastIndex(loc, "/"); i >= 0 {
		return loc[i+1:]
	}
	return ""
}
