// SPDX-FileCopyrightText: 2026 Angels' Ware
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
	"fmt"
	"strconv"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	hfnet "github.com/dazyflow/dazyflow/drops/net"
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

// smhiGet fetches the point forecast for a coordinate. SMHI puts lon BEFORE lat
// in the path. currentOnly adds &timeseries=1 to return just the current step;
// otherwise the full ~10-day series comes back.
func smhiGet(ctx context.Context, job core.Job, lat, lon float64, currentOnly bool) (int, []byte, error) {
	q := "?parameters=" + smhiParams
	if currentOnly {
		q += "&timeseries=1"
	}
	url := fmt.Sprintf("%s/lon/%s/lat/%s/data.json%s", forecastBase, fmtCoord6(lon), fmtCoord6(lat), q)
	timeoutMS := params.TimeoutMS(job, 15000)
	status, body, _, err := hfnet.Do(ctx, "GET", url, nil, nil, timeoutMS, maxResponseBytes)
	return status, body, err
}

// httpFailure maps a transport error or non-2xx response to an error Result,
// returning nil on success. A 404 means the coordinate is outside SMHI's model
// domain (the API answers "Requested point is out of bounds").
func httpFailure(job core.Job, status int, body []byte, err error) *core.Result {
	if r := geoloc.TransportFailure(job, "smhi", "SMHI", err); r != nil {
		return r
	}
	if status == 404 {
		r := params.Err(job, "out_of_domain",
			"SMHI has no forecast for that coordinate — its model covers the Nordic region and the surrounding area, so the point may be outside it.")
		return &r
	}
	if status < 200 || status >= 300 {
		r := params.Err(job, "smhi_error", fmt.Sprintf("SMHI returned %d: %s", status, params.Truncate(string(body), 200)))
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
