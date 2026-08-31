// SPDX-FileCopyrightText: 2026 Angels' Ware
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
// can drive it). Coordinate parsing, unit symbols and number formatting are
// shared with the other location connectors in drops/internal/geoloc. The
// hosts are fixed (not tenant-supplied), but the dial still goes through the
// shared SSRF guard (net.Do → SafeHTTPClient).
package weather

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
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

// normalizeUnits maps the units param to a value the API accepts, defaulting
// to metric (°C, m/s). An unrecognised value falls back to metric rather than
// forwarding garbage the API would reject. OpenWeather is the only connector
// with a Kelvin ("standard") option, so this stays local.
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
	if r := geoloc.TransportFailure(job, "owm", "OpenWeather", err); r != nil {
		return r
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
