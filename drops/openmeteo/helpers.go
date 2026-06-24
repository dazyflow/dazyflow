// Package openmeteo hosts the Open-Meteo connector: read the current
// conditions or a multi-day daily forecast for a latitude/longitude from
// Open-Meteo's Forecast API.
//
// Open-Meteo needs NO API key for non-commercial use — the free endpoint
// (api.open-meteo.com) answers key-less. A key is only required for
// Open-Meteo's commercial (paid) plan, which routes through a separate host
// (customer-api.open-meteo.com) and carries the key as an `apikey` query
// param. The key is therefore an OPTIONAL per-tenant ConnectionField: leave
// it blank and the drops call the free endpoint; set it and they call the
// commercial one. Deciding whether a given use is commercial — and supplying
// a key when it is — is the user's responsibility, mirrored in the field copy.
//
// The coordinate can be typed on the node as separate Latitude / Longitude
// numbers, or wired in from another step as a single "lat,lon" text value on
// the Coordinate input (so a geocode step, a form field, or a device's GPS
// can drive it). The hosts are fixed (not tenant-supplied), but the dial still
// goes through the shared SSRF guard (net.Do → SafeHTTPClient).
package openmeteo

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

// The Forecast endpoint on both hosts: the key-less free host and the
// commercial host that an `apikey` unlocks. Both current and forecast drops
// hit /v1/forecast and select fields with the query. They're vars, not
// consts, so tests can point them at a local httptest server.
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

// normalizeUnits maps the units param to metric or imperial, defaulting to
// metric. Open-Meteo has no Kelvin ("standard") option, so anything other than
// "imperial" falls back to metric rather than forwarding garbage.
func normalizeUnits(u string) string {
	if strings.ToLower(strings.TrimSpace(u)) == "imperial" {
		return "imperial"
	}
	return "metric"
}

// tempParam / windParam map a normalized units value to the query values
// Open-Meteo expects (temperature_unit, wind_speed_unit).
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

// tempUnit / speedUnit return the display symbols for a units value, so a
// human-readable summary reads "12.3°C" / "3.4 m/s" rather than a bare number.
func tempUnit(units string) string {
	if units == "imperial" {
		return "°F"
	}
	return "°C"
}

func speedUnit(units string) string {
	if units == "imperial" {
		return "mph"
	}
	return "m/s"
}

// baseQuery builds the latitude/longitude/unit query shared by both drops.
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

// omGet performs one GET against the chosen Open-Meteo host with the given
// query. When a key is configured it is appended as `apikey` and the request
// goes to the commercial host. Transport and non-2xx handling is the shared
// httpFailure epilogue.
func omGet(ctx context.Context, job core.Job, q url.Values) (int, []byte, error) {
	base, key := endpointFor(job)
	if key != "" {
		q.Set("apikey", key)
	}
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	status, body, _, err := hfnet.Do(ctx, "GET", base+"?"+q.Encode(), nil, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// extractOMError pulls the human message out of an Open-Meteo error body
// ({"error":true,"reason":"Latitude must be in range …"}) so the real reason
// reaches the user instead of a bare status. Falls back to a truncated raw body.
func extractOMError(body []byte) string {
	var e struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &e); err == nil && e.Reason != "" {
		return e.Reason
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// httpFailure maps a transport error or non-2xx response to an error Result,
// returning nil on success — the shared epilogue of both drops. A 401 only
// happens on the commercial host: the configured key is wrong (or the free
// endpoint would have answered without one).
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach Open-Meteo — the request was blocked by the egress policy.", err.Error())
			return &r
		}
		r := params.Err(job, "openmeteo_http_error", "Couldn't reach Open-Meteo: "+err.Error())
		return &r
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

// --- WMO weather codes -------------------------------------------------------

// wmo maps WMO weather-interpretation codes (the integer weather_code
// Open-Meteo returns) to a human phrase.
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

// classFor reduces a WMO code to a short, branchable class word.
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

// num1 formats a number to one decimal ("12.3"); num0 rounds to a whole
// number ("12") for the coarser daily min/max range.
func num1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
func num0(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

// capitalizeFirst upper-cases the first ASCII letter of a phrase so
// "clear sky" reads "Clear sky" at the start of a summary.
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
