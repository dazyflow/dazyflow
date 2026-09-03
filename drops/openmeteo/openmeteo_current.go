// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package openmeteo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dazyflow/dazyflow/core"
	"github.com/dazyflow/dazyflow/drops/internal/geoloc"
	"github.com/dazyflow/dazyflow/drops/internal/params"
	"github.com/dazyflow/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "openmeteo_current",
			Version:     "1.0",
			Label:       "Open-Meteo",
			Subtitle:    "Current conditions",
			Summary:     "Get the current weather at a coordinate from Open-Meteo — no API key for non-commercial use.",
			Description: "Look up the weather right now for any point on the map using Open-Meteo's free forecast — no account or API key for personal, non-commercial use. Give it a coordinate — type the Latitude and Longitude, or connect a \"lat,lon\" value in from a Location/Geocode step — and it returns a one-line Summary plus the Temperature and a Conditions word (Clear, Rain, Snow…) you can branch on, and the full response as JSON. For commercial use, add your Open-Meteo API key on the integration page and it switches to the paid endpoint.",
			Integration: "Open-Meteo",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/openmeteo.svg",
			Color:       "#f5a623",
			Provider:    "internal",
			Tags:        []string{"weather", "open-meteo", "openmeteo", "forecast", "temperature", "coordinate", "lat", "lon", "current", "conditions", "free", "no key"},
			Examples: []core.ParamsExample{
				{
					Title:  "Weather in Stockholm (°C)",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686,"units":"metric"}`),
				},
				{
					Title:  "Weather in New York (°F)",
					Params: json.RawMessage(`{"lat":40.7128,"lon":-74.006,"units":"imperial"}`),
					Notes:  "Summary reads in °F and mph; Conditions is a single word (Clear, Clouds, Rain) that's easy to branch on.",
				},
			},
			ConnectionFields: []core.ConnectionField{
				{Key: "api_key", Label: "API key", Secret: true, Help: "Optional — only for Open-Meteo's commercial (paid) plan. Leave blank for free non-commercial use."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Light rain, 4.2°C (feels 1.8°C), humidity 87%, wind 5.1 m/s"`)},
				{Port: "temperature", Label: "Temperature", MIME: []string{"text/plain"}, Example: json.RawMessage(`"4.2"`)},
				{Port: "conditions", Label: "Conditions", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Light rain"`)},
				{Port: "weather", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude of the point, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude of the point, -180..180. Overridden by the Coordinate input."},
					"units":{"type":"string","enum":["metric","imperial"],"enumNames":["Metric (°C, m/s)","Imperial (°F, mph)"],"default":"metric","title":"Units","description":"metric = °C + m/s, imperial = °F + mph."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			// Reading the weather has no side effects — safe to retry on a blip.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCurrent,
	})
}

// omCurrent is the subset of the /v1/forecast response (current block) the
// summary uses. Fields the API omits decode to their zero value; the JSON
// output pin carries the full, untrimmed response so nothing is lost.
type omCurrent struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		Apparent    float64 `json:"apparent_temperature"`
		Humidity    float64 `json:"relative_humidity_2m"`
		WeatherCode int     `json:"weather_code"`
		WindSpeed   float64 `json:"wind_speed_10m"`
	} `json:"current"`
}

// executeCurrent fetches the current conditions for the resolved coordinate
// and emits a readable summary, the bare temperature and conditions word, and
// the full response as JSON.
func executeCurrent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := geoloc.ResolveLatLon(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	units := normalizeUnits(params.StringDefault(job.Params, "units", "metric"))
	q := baseQuery(lat, lon, units)
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m")

	status, body, err := omGet(ctx, job, q)
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var doc map[string]any
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		return params.ErrDetails(job, "openmeteo_error", "Open-Meteo returned an unexpected response.", uerr.Error()), nil
	}
	var cur omCurrent
	_ = json.Unmarshal(body, &cur) // best-effort: drives the human summary

	conditions := classFor(cur.Current.WeatherCode)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary":     {MIME: "text/plain", Inline: currentSummary(cur, units)},
			"temperature": {MIME: "text/plain", Inline: geoloc.Num1(cur.Current.Temperature)},
			"conditions":  {MIME: "text/plain", Inline: conditions},
			"weather":     {MIME: "application/json", Inline: doc},
		},
	}, nil
}

// currentSummary renders the current conditions as one human line, e.g.
// "Partly cloudy, 12.3°C (feels 11.1°C), humidity 64%, wind 3.4 m/s".
func currentSummary(c omCurrent, units string) string {
	desc := geoloc.CapitalizeFirst(wmo[c.Current.WeatherCode])
	tu, su := geoloc.TempUnit(units), geoloc.SpeedUnit(units)
	if desc == "" {
		return fmt.Sprintf("%s%s (feels %s%s), humidity %s%%, wind %s %s",
			geoloc.Num1(c.Current.Temperature), tu, geoloc.Num1(c.Current.Apparent), tu, geoloc.Num0(c.Current.Humidity), geoloc.Num1(c.Current.WindSpeed), su)
	}
	return fmt.Sprintf("%s, %s%s (feels %s%s), humidity %s%%, wind %s %s",
		desc, geoloc.Num1(c.Current.Temperature), tu, geoloc.Num1(c.Current.Apparent), tu, geoloc.Num0(c.Current.Humidity), geoloc.Num1(c.Current.WindSpeed), su)
}
