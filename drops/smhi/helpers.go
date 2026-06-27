// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package smhi hosts the SMHI connector: current conditions and a daily
// forecast for a coordinate, from SMHI's free Open Data Meteorological
// Forecasts API — no account or API key.
//
// It uses the live snow1g v1 point endpoint:
//
//	…/api/category/snow1g/version/1/geotype/point/lon/{lon}/lat/{lat}/data.json?parameters=…
//
// Each timeSeries entry carries a `time` and a flat `data` map of the requested
// parameters; `symbol_code` is SMHI's Wsymb2 weather symbol (1–27). Omitting the
// `timeseries` query returns the full ~10-day hourly series; `&timeseries=1`
// returns just the current step. The model covers the Nordic region and the
// surrounding area; a point outside it returns "out of bounds".
//
// Output coordinates use the same "lat,lon" shape the OpenWeather and
// OpenStreetMap drops speak, so a Location/Geocode pick wires straight in.
package smhi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	hfnet "git.sr.ht/~klahr/dazyflow/drops/net"
)

// forecastBase is SMHI's snow1g point-forecast endpoint root; the full URL
// appends /lon/{lon}/lat/{lat}/data.json plus the parameters query. A var so
// tests can point at a local server.
var forecastBase = "https://opendata-download-metfcst.smhi.se/api/category/snow1g/version/1/geotype/point"

// smhiParams is the comma-separated parameter set the drops request.
const smhiParams = "air_temperature,wind_speed,relative_humidity,symbol_code,precipitation_amount_mean,probability_of_precipitation"

const maxResponseBytes = 8 << 20 // 8 MiB

// fmtCoord6 formats a coordinate to 6 decimals (the model snaps it to its grid).
func fmtCoord6(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// parseLatLon splits a "lat,lon" string into two range-checked floats.
func parseLatLon(s string) (lat, lon float64, err error) {
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
	if lat < -90 || lat > 90 {
		return 0, 0, fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", lat)
	}
	if lon < -180 || lon > 180 {
		return 0, 0, fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lon)
	}
	return lat, lon, nil
}

// numParam reads a numeric param as float64 — never a numeric string.
func numParam(p map[string]any, key string) (float64, bool) {
	switch n := p[key].(type) {
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

// resolveCoord determines the coordinate: the Coordinate input ("lat,lon") wins
// when wired with text, otherwise the Latitude/Longitude params.
func resolveCoord(job core.Job) (lat, lon float64, err error) {
	txt, ok := params.TextInputOr(job, "coordinate", "")
	if !ok {
		return 0, 0, errors.New(`'Coordinate' input must be text like "59.33,18.07"`)
	}
	if s := strings.TrimSpace(txt); s != "" {
		return parseLatLon(s)
	}
	la, laOK := numParam(job.Params, "lat")
	lo, loOK := numParam(job.Params, "lon")
	if !laOK || !loOK {
		return 0, 0, errors.New(`set Latitude and Longitude, or wire a "lat,lon" value into the Coordinate input`)
	}
	if la < -90 || la > 90 {
		return 0, 0, fmt.Errorf("latitude %g is out of range (must be between -90 and 90)", la)
	}
	if lo < -180 || lo > 180 {
		return 0, 0, fmt.Errorf("longitude %g is out of range (must be between -180 and 180)", lo)
	}
	return la, lo, nil
}

// smhiGet fetches the point forecast for a coordinate. SMHI puts lon BEFORE lat
// in the path. currentOnly adds &timeseries=1 to return just the current step;
// otherwise the full ~10-day series comes back.
func smhiGet(ctx context.Context, job core.Job, lat, lon float64, currentOnly bool) (int, []byte, error) {
	q := "?parameters=" + smhiParams
	if currentOnly {
		q += "&timeseries=1"
	}
	url := fmt.Sprintf("%s/lon/%s/lat/%s/data.json%s", forecastBase, fmtCoord6(lon), fmtCoord6(lat), q)
	timeoutMS := params.IntDefault(job.Params, "timeout_ms", 15000)
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	status, body, _, err := hfnet.Do(ctx, "GET", url, nil, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// httpFailure maps a transport error or non-2xx response to an error Result,
// returning nil on success. A 404 means the coordinate is outside SMHI's model
// domain (the API answers "Requested point is out of bounds").
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if err != nil {
		if hfnet.IsSSRFError(err) {
			r := params.ErrDetails(job, "egress_blocked",
				"Couldn't reach SMHI — the request was blocked by the egress policy.", err.Error())
			return &r
		}
		r := params.Err(job, "smhi_http_error", "Couldn't reach SMHI: "+err.Error())
		return &r
	}
	if status == 404 {
		r := params.Err(job, "out_of_domain",
			"SMHI has no forecast for that coordinate — its model covers the Nordic region and the surrounding area, so the point may be outside it.")
		return &r
	}
	if status < 200 || status >= 300 {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200]
		}
		r := params.Err(job, "smhi_error", fmt.Sprintf("SMHI returned %d: %s", status, s))
		return &r
	}
	return nil
}

// --- response shapes ---------------------------------------------------------

// smhiResponse is the snow1g point document.
type smhiResponse struct {
	TimeSeries []smhiEntry `json:"timeSeries"`
}

// smhiEntry is one forecast step: a valid time plus a flat parameter map.
type smhiEntry struct {
	Time string         `json:"time"`
	Data map[string]any `json:"data"`
}

// num returns a numeric parameter from the step's data map (air_temperature,
// wind_speed, symbol_code, …). Values decode to float64 (JSON numbers).
func (e smhiEntry) num(name string) (float64, bool) {
	switch v := e.Data[name].(type) {
	case float64:
		return v, true
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f, true
		}
	}
	return 0, false
}

// wsymb2 maps SMHI's weather-symbol codes (1–27) to a human phrase.
var wsymb2 = map[int]string{
	1: "Clear sky", 2: "Nearly clear sky", 3: "Variable cloudiness", 4: "Halfclear sky",
	5: "Cloudy sky", 6: "Overcast", 7: "Fog",
	8: "Light rain showers", 9: "Moderate rain showers", 10: "Heavy rain showers",
	11: "Thunderstorm",
	12: "Light sleet showers", 13: "Moderate sleet showers", 14: "Heavy sleet showers",
	15: "Light snow showers", 16: "Moderate snow showers", 17: "Heavy snow showers",
	18: "Light rain", 19: "Moderate rain", 20: "Heavy rain",
	21: "Thunder",
	22: "Light sleet", 23: "Moderate sleet", 24: "Heavy sleet",
	25: "Light snowfall", 26: "Moderate snowfall", 27: "Heavy snowfall",
}

// classFor reduces a Wsymb2 code to a short, branchable class word.
func classFor(code int) string {
	switch {
	case code <= 2:
		return "Clear"
	case code <= 6:
		return "Clouds"
	case code == 7:
		return "Fog"
	case code == 11 || code == 21:
		return "Thunder"
	case (code >= 12 && code <= 14) || (code >= 22 && code <= 24):
		return "Sleet"
	case (code >= 15 && code <= 17) || (code >= 25 && code <= 27):
		return "Snow"
	default: // 8–10, 18–20
		return "Rain"
	}
}

func num1(f float64) string { return strconv.FormatFloat(f, 'f', 1, 64) }
func num0(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }

// capitalizeFirst upper-cases the first ASCII letter of a phrase.
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
