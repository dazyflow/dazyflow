// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package geoloc holds the coordinate, unit and formatting helpers the
// location/weather connectors carried as copy-pasted boilerplate: parsing and
// range-checking a "lat,lon" string, resolving the Coordinate input against
// the Latitude/Longitude params, the display symbols for a units value, the
// two number formats a human summary uses, and the SSRF/transport prologue of
// every httpFailure epilogue.
//
// It lives under drops/internal/ so only sibling connector packages import it.
// weather (OpenWeather), openmeteo (Open-Meteo), smhi and geo (OpenStreetMap)
// each kept their own copy of these — the bodies and the user-facing error
// strings never diverged, so they live here once. The strings are part of the
// contract: connector tests assert on them, so keep them verbatim.
//
// What stays per-connector is anything provider-shaped: which host to dial,
// how the provider spells its units query, which status codes get a bespoke
// message, and the weather-code tables (WMO vs Wsymb2).
package geoloc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

// Parse splits a "lat,lon" string (e.g. "59.33,18.07") into two range-checked
// floats. Whitespace around either part is tolerated. A missing comma, a
// non-numeric part, or an out-of-range value is a clear user error, reported
// as such.
func Parse(s string) (lat, lon float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("coordinate %q must be \"lat,lon\" — e.g. 59.33,18.07", s)
	}
	lat, err = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("latitude %q isn't a number", strings.TrimSpace(parts[0]))
	}
	lon, err = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, fmt.Errorf("longitude %q isn't a number", strings.TrimSpace(parts[1]))
	}
	if err := CheckRange(lat, lon); err != nil {
		return 0, 0, err
	}
	return lat, lon, nil
}

// CheckRange rejects an obviously bad coordinate so a lookup fails fast with a
// readable message instead of a remote 400.
func CheckRange(lat, lon float64) error {
	if lat < -90 || lat > 90 {
		return fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", lat)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lon)
	}
	return nil
}

// Num reads a numeric param as float64, accepting the Go number types a
// decoded JSON document can carry. It deliberately does NOT parse numeric
// strings: a coordinate is a number, and refusing strings keeps a stray text
// value (e.g. a mis-wired param) from being mistaken for a valid lat/lon.
func Num(p map[string]any, key string) (float64, bool) {
	v, ok := p[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		if f, err := n.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ResolveLatLon determines the target coordinate for a weather-style lookup:
// the Coordinate input ("lat,lon") wins when wired with text, otherwise the
// Latitude/Longitude params. Both paths are range-checked.
//
// Connectors whose coordinate comes from somewhere else (geo's map pin, say)
// resolve it themselves and call Parse.
func ResolveLatLon(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return Parse(s)
	}
	latOK, lonOK := false, false
	lat, latOK = Num(job.Params, "lat")
	lon, lonOK = Num(job.Params, "lon")
	if !latOK || !lonOK {
		return 0, 0, errors.New(`set Latitude and Longitude, or wire a "lat,lon" value into the Coordinate input`)
	}
	if err := CheckRange(lat, lon); err != nil {
		return 0, 0, err
	}
	return lat, lon, nil
}

// Fmt renders a "lat,lon" string, trimming trailing zeros so a tidy point
// stays tidy ("59.3293,18.0686", not "59.32930000,18.06860000"). This is the
// shape every connector's Coordinate input accepts, so a geocode or picker
// wires straight into a weather lookup.
func Fmt(lat, lon float64) string {
	return strconv.FormatFloat(lat, 'f', -1, 64) + "," + strconv.FormatFloat(lon, 'f', -1, 64)
}

// TempUnit returns the display symbol for a units value, so a human-readable
// summary reads "12.3°C" rather than a bare number. "standard" (Kelvin) only
// exists on OpenWeather; connectors without it never pass it.
func TempUnit(units string) string {
	switch units {
	case "imperial":
		return "°F"
	case "standard":
		return "K"
	default:
		return "°C"
	}
}

// SpeedUnit returns the display symbol for wind speed ("3.4 m/s").
func SpeedUnit(units string) string {
	if units == "imperial" {
		return "mph"
	}
	return "m/s"
}

// Num1 formats a number to one decimal ("12.3"); Num0 rounds to a whole
// number ("12") for the coarser daily min/max range.
func Num1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }

// Num0 rounds to a whole number — see Num1.
func Num0(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

// CapitalizeFirst upper-cases the first rune of a phrase so "clear sky" reads
// "Clear sky" at the start of a summary. ASCII-only, which covers the English
// descriptions; localized langs keep their own casing.
func CapitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// TransportFailure maps a transport error to an error Result — the shared
// prologue of every location connector's httpFailure. An SSRF/egress refusal
// gets its own code so the run detail view can explain it; anything else is
// "<code>_http_error". Returns nil when err is nil, so callers fall through to
// their own status-code handling.
//
// label is the human service name ("OpenWeather", "SMHI") and code the
// connector's error-code prefix ("owm", "smhi").
func TransportFailure(job core.Job, code, label string, err error) *core.Result {
	if err == nil {
		return nil
	}
	if hfnet.IsSSRFError(err) {
		r := params.ErrDetails(job, "egress_blocked",
			"Couldn't reach "+label+" — the request was blocked by the egress policy.", err.Error())
		return &r
	}
	r := params.Err(job, code+"_http_error", "Couldn't reach "+label+": "+err.Error())
	return &r
}

// probeBodyCap bounds what Probe buffers — a verification response is a few
// hundred bytes; 64 KiB is headroom that still refuses an unbounded body.
const probeBodyCap = 1 << 16

// probeTimeout bounds a connection check. The Apps page waits on it
// synchronously, so it stays short.
const probeTimeout = 10 * time.Second

// Probe performs the one-shot GET a connection verifier uses to check
// credentials, returning the status and a capped body. Egress policy is
// checked before dialing and the dial goes through the same SSRF-guarded
// client as every other connector, so a verifier can't be used to reach an
// address a drop couldn't.
//
// label is the human service name used in the dial-failure message ("could not
// reach OpenWeather: …"); an egress refusal is returned verbatim, since it
// already explains itself and reaches the Apps page unchanged.
//
// A non-2xx status is NOT an error — it comes back as a status for the caller
// to interpret, since what a 401 means differs per provider.
func Probe(ctx context.Context, label, url string) (status int, body []byte, err error) {
	if err := hfnet.EgressAllowedFor(ctx, url); err != nil {
		return 0, nil, err
	}
	reqCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("could not build request: %w", err)
	}
	resp, err := hfnet.SafeHTTPClient(probeTimeout, hfnet.PrivateEgressAllowed()).Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("could not reach %s: %w", label, err)
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, probeBodyCap))
	return resp.StatusCode, body, nil
}
