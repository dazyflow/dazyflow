// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package openmeteo hosts the Open-Meteo connector: current conditions or a
// multi-day daily forecast for a latitude/longitude.
//
// Open-Meteo needs no API key for non-commercial use; a key is only required for
// the paid plan, which routes through a separate host and carries it as an
// `apikey` query param. The key is therefore an OPTIONAL per-tenant
// ConnectionField — blank calls the free endpoint, set calls the commercial one.
// Deciding whether a use is commercial is the user's responsibility, and the
// field copy says so.
//
// The coordinate can be typed as separate Latitude / Longitude numbers or wired
// in as a single "lat,lon" text value, so a geocode step, a form field or a
// device's GPS can drive it. Parsing, unit symbols and number formatting are
// shared with the other location connectors in drops/internal/geoloc. The hosts
// are fixed, but the dial still goes through the shared SSRF guard.
package openmeteo

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
)

var (
	freeURL       = "https://api.open-meteo.com/v1/forecast"
	commercialURL = "https://customer-api.open-meteo.com/v1/forecast"
)

// maxResponseBytes caps how much of a response we buffer. A 16-day daily
// forecast is well under this; the cap is generous headroom that still
// refuses an unbounded body.
const maxResponseBytes = 2 << 20 // 2 MiB

// resolveKey reads the optional Open-Meteo API key the engine injected from
// the tenant's connection (ConnectionField "api_key"). Empty is the normal
// case — the free non-commercial endpoint needs no key.
func resolveKey(job core.Job) string {
	return strings.TrimSpace(params.StringDefault(job.Params, "api_key", ""))
}

func normalizeUnits(u string) string {
	if strings.ToLower(strings.TrimSpace(u)) == "imperial" {
		return "imperial"
	}
	return "metric"
}

func tempParam(units string) string {
	if units == "imperial" {
		return "fahrenheit"
	}
	return "celsius"
}

func windParam(units string) string {
	if units == "imperial" {
		return "mph"
	}
	return "ms"
}

func baseQuery(lat, lon float64, units string) url.Values {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(lat, 'f', -1, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', -1, 64))
	q.Set("temperature_unit", tempParam(units))
	q.Set("wind_speed_unit", windParam(units))
	return q
}

// endpointFor selects the host and key: a configured key routes to the
// commercial host, otherwise the free non-commercial host (key empty).
func endpointFor(job core.Job) (base, key string) {
	if key = resolveKey(job); key != "" {
		return commercialURL, key
	}
	return freeURL, ""
}

func omGet(ctx context.Context, job core.Job, q url.Values) (int, []byte, error) {
	base, key := endpointFor(job)
	if key != "" {
		q.Set("apikey", key)
	}
	timeoutMS := params.TimeoutMS(job, 15000)
	status, body, _, err := hfnet.Do(ctx, "GET", base+"?"+q.Encode(), nil, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// extractOMError pulls the human message out of an Open-Meteo error body
// ({"error":true,"reason":"Latitude must be in range …"}) so the real reason
// reaches the user instead of a bare status. Falls back to a truncated raw body.
func extractOMError(body []byte) string {
	return params.JSONFieldMessage(body, "reason", 300)
}

// httpFailure maps a transport error or non-2xx response to an error Result,
// returning nil on success — the shared epilogue of both drops. A 401 only
// happens on the commercial host: the configured key is wrong (or the free
// endpoint would have answered without one).
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if r := geoloc.TransportFailure(job, "openmeteo", "Open-Meteo", err); r != nil {
		return r
	}
	if status == 401 {
		msg := "Open-Meteo rejected the API key (401). The key is only for the commercial plan — check that it's correct, or clear it to use the free non-commercial endpoint."
		if detail := extractOMError(body); detail != "" {
			msg = "Open-Meteo rejected the API key: " + detail
		}
		r := params.Err(job, "auth", msg)
		return &r
	}
	return params.HTTPFailure(job, "openmeteo", "Open-Meteo", status, body, nil, extractOMError)
}

var wmo = map[int]string{
	0:  "Clear sky",
	1:  "Mainly clear",
	2:  "Partly cloudy",
	3:  "Overcast",
	45: "Fog",
	48: "Depositing rime fog",
	51: "Light drizzle", 53: "Moderate drizzle", 55: "Dense drizzle",
	56: "Light freezing drizzle", 57: "Dense freezing drizzle",
	61: "Slight rain", 63: "Moderate rain", 65: "Heavy rain",
	66: "Light freezing rain", 67: "Heavy freezing rain",
	71: "Slight snowfall", 73: "Moderate snowfall", 75: "Heavy snowfall",
	77: "Snow grains",
	80: "Slight rain showers", 81: "Moderate rain showers", 82: "Violent rain showers",
	85: "Slight snow showers", 86: "Heavy snow showers",
	95: "Thunderstorm",
	96: "Thunderstorm with slight hail", 99: "Thunderstorm with heavy hail",
}

func classFor(code int) string {
	switch {
	case code <= 1:
		return "Clear"
	case code <= 3:
		return "Clouds"
	case code == 45 || code == 48:
		return "Fog"
	case code >= 51 && code <= 57:
		return "Drizzle"
	case (code >= 61 && code <= 67) || (code >= 80 && code <= 82):
		return "Rain"
	case (code >= 71 && code <= 77) || code == 85 || code == 86:
		return "Snow"
	case code >= 95 && code <= 99:
		return "Thunder"
	default:
		return ""
	}
}
