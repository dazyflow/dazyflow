// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package weather hosts the OpenWeather connector: read the current
// conditions or a multi-day forecast for a latitude/longitude.
//
// It uses OpenWeather's FREE endpoints — Current Weather (/data/2.5/weather)
// and the 5-day / 3-hour Forecast (/data/2.5/forecast) — which any standard
// API key can call without the paid "One Call by Call" subscription. (The
// One Call 3.0 endpoint is paywalled and returns 401 for free keys, which is
// why this connector deliberately avoids it.)
//
// Auth is a single per-tenant ConnectionField — the OpenWeather API key (the
// "appid"), configured once on the integration page rather than typed on every
// node. The engine injects the configured connection into each node's unset
// params at run time (injectConnectionDefaults), so flows carry only the
// per-use fields (which coordinate, which units) — the same shape as the
// Home Assistant and ntfy connectors.
//
// The coordinate can be typed on the node as separate Latitude / Longitude
// numbers, or wired in from another step as a single "lat,lon" text value on
// the Coordinate input (so a geocode step, a form field, or a device's GPS
// can drive it). The hosts are fixed (not tenant-supplied), but the dial still
// goes through the shared SSRF guard (net.Do → SafeHTTPClient).
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// The free Current Weather and 5-day/3-hour Forecast endpoints. They're vars,
// not consts, so tests can point them at a local httptest server.
var (
	currentURL  = "https://api.openweathermap.org/data/2.5/weather"
	forecastURL = "https://api.openweathermap.org/data/2.5/forecast"
)

// maxResponseBytes caps how much of a response we buffer. A full 5-day/3-hour
// forecast (40 slots) is well under 256 KiB; the cap is generous headroom that
// still refuses an unbounded body.
const maxResponseBytes = 2 << 20 // 2 MiB

// resolveKey reads the OpenWeather API key the engine injected from the
// tenant's connection (ConnectionField "api_key"). Empty means OpenWeather
// isn't connected yet — callers surface that with a pointed message.
func resolveKey(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "api_key", ""))
}

// floatParam reads a numeric param as float64, accepting the Go number types a
// decoded JSON document can carry. It deliberately does NOT parse numeric
// strings: a coordinate is a number, and refusing strings keeps a stray text
// value (e.g. a mis-wired param) from being mistaken for a valid lat/lon.
func floatParam(p map[string]any, key string) (float64, bool) {
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

// parseCoordinate splits a "lat,lon" string (e.g. "59.33,18.07") into two
// floats. Whitespace around either part is tolerated. A missing comma or a
// non-numeric part is a clear user error, reported as such.
func parseCoordinate(s string) (lat, lon float64, err error) {
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
	return lat, lon, nil
}

// resolveCoord determines the target coordinate: the Coordinate input
// ("lat,lon") wins when wired with text, otherwise the Latitude/Longitude
// params. The result is range-checked so an obviously bad coordinate fails
// fast with a readable message instead of a remote 400.
func resolveCoord(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		if lat, lon, err = parseCoordinate(s); err != nil {
			return 0, 0, err
		}
	} else {
		var latOK, lonOK bool
		lat, latOK = floatParam(job.Params, "lat")
		lon, lonOK = floatParam(job.Params, "lon")
		if !latOK || !lonOK {
			return 0, 0, errors.New(`set Latitude and Longitude, or wire a "lat,lon" value into the Coordinate input`)
		}
	}
	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lon)
	}
	return lat, lon, nil
}

// normalizeUnits maps the units param to a value the API accepts, defaulting
// to metric (°C, m/s). An unrecognised value falls back to metric rather than
// forwarding garbage the API would reject.
func normalizeUnits(u string) string {
	switch strings.ToLower(strings.TrimSpace(u)) {
	case "imperial":
		return "imperial"
	case "standard":
		return "standard"
	default:
		return "metric"
	}
}

// tempUnit / speedUnit return the display symbols for a units value, so a
// human-readable summary reads "12.3°C" / "3.4 m/s" rather than a bare number.
func tempUnit(u string) string {
	switch u {
	case "imperial":
		return "°F"
	case "standard":
		return "K"
	default:
		return "°C"
	}
}

func speedUnit(u string) string {
	if u == "imperial" {
		return "mph"
	}
	return "m/s"
}

// owmGet performs one GET against an OpenWeather 2.5 endpoint for the given
// coordinate. It returns the HTTP status and raw body; transport and non-2xx
// handling is the shared httpFailure epilogue.
func owmGet(ctx context.Context, job core.Job, endpoint string, lat, lon float64) (int, []byte, error) {
	key := resolveKey(job)
	if key == "" {
		// Callers check resolveKey before dialing; this is a belt-and-braces
		// guard so we never send an empty appid to the API.
		return 0, nil, errors.New("OpenWeather API key is not set")
	}
	units := normalizeUnits(params.StringDefault(job.Params, "units", "metric"))

	q := url.Values{}
	q.Set("lat", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("lon", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("appid", key)
	q.Set("units", units)
	if lang := strings.TrimSpace(params.StringDefault(job.Params, "lang", "")); lang != "" {
		q.Set("lang", lang)
	}

	timeoutMS := params.TimeoutMS(job, 15000)
	status, body, _, err := hfnet.Do(ctx, "GET", endpoint+"?"+q.Encode(), nil, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// extractOWMError pulls the human message out of an OpenWeather error body
// ({"cod":401,"message":"Invalid API key. ..."}) so the real reason reaches
// the user instead of a bare status. Falls back to a truncated raw body.
func extractOWMError(body []byte) string {
	return params.JSONFieldMessage(body, "message", 300)
}

// httpFailure maps a transport error or non-2xx response to an error Result,
// returning nil on success — the shared epilogue of both weather drops. A 401
// on the free 2.5 endpoints means the key is wrong or not yet activated (new
// keys can take a couple of hours).
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach OpenWeather — the request was blocked by the egress policy.", err.Error())
			return &r
		}
		r := params.Err(job, "owm_http_error", "Couldn't reach OpenWeather: "+err.Error())
		return &r
	}
	if status == 401 {
		// Lead with OpenWeather's own message when present — so the run detail
		// view shows the actionable reason, not a generic 401.
		msg := "OpenWeather rejected the API key (401). Check that the key is correct and active — a newly created key can take a couple of hours to start working."
		if detail := extractOWMError(body); detail != "" {
			msg = "OpenWeather rejected the API key: " + detail
		}
		r := params.Err(job, "auth", msg)
		return &r
	}
	return params.HTTPFailure(job, "owm", "OpenWeather", status, body, nil, extractOWMError)
}

// owmWeather is the {id, main, description, icon} object that appears in both
// the current observation and each forecast slot. Main is the short class
// ("Clear", "Rain") that's handy for branching; Description is the long phrase.
type owmWeather struct {
	ID          int    `json:"id"`
	Main        string `json:"main"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
}

// num1 formats a number to one decimal ("12.3"); num0 rounds to a whole
// number ("12") for the coarser daily min/max range.
func num1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
func num0(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

// capitalizeFirst upper-cases the first rune of a phrase so "clear sky"
// reads "Clear sky" at the start of a summary. ASCII-only, which covers the
// English descriptions; localized langs keep their own casing.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
