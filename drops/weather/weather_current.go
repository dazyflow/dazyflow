// SPDX-FileCopyrightText: 2026 Angels' Ware
// SPDX-License-Identifier: AGPL-3.0-or-later

package weather

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
			ID:          "weather_current",
			Version:     "1.0",
			Label:       "Weather",
			Subtitle:    "Current conditions",
			Summary:     "Get the current weather at a latitude/longitude from OpenWeather.",
			Description: "Look up the weather right now for a point on the map. Give it a coordinate — type the Latitude and Longitude, or connect a \"lat,lon\" value in from another step (a geocode, a form field, a device's GPS) — and it returns a one-line Summary plus the Temperature and a Conditions word (Clear, Rain, Snow…) you can branch on, and the full observation as JSON. Uses OpenWeather's free Current Weather API — any standard key works, no paid subscription.",
			Integration: "OpenWeather",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/openweather.svg",
			Color:       "#eb6e4b",
			Provider:    "internal",
			Tags:        []string{"weather", "openweather", "openweathermap", "forecast", "temperature", "coordinate", "lat", "lon", "current", "conditions"},
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
				{Key: "api_key", Label: "API key", Secret: true, Required: true, Help: "Your OpenWeather API key. The free plan works — no paid subscription needed."},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Light rain, 4.2°C (feels 1.8°C), humidity 87%, wind 5.1 m/s"`)},
				{Port: "temperature", Label: "Temperature", MIME: []string{"text/plain"}, Example: json.RawMessage(`"4.2"`)},
				{Port: "conditions", Label: "Conditions", MIME: []string{"text/plain"}, Example: json.RawMessage(`"Rain"`)},
				{Port: "weather", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude of the point, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude of the point, -180..180. Overridden by the Coordinate input."},
					"units":{"type":"string","enum":["metric","imperial","standard"],"enumNames":["Metric (°C, m/s)","Imperial (°F, mph)","Kelvin (K, m/s)"],"default":"metric","title":"Units","description":"metric = °C + m/s, imperial = °F + mph, standard = K + m/s."},
					"lang":{"type":"string","title":"Language","description":"Optional ISO language code for the weather Description (e.g. sv, de, es). Defaults to English."},
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

// owmObservation is the subset of the /data/2.5/weather response the summary
// uses. Fields the API omits decode to their zero value; the JSON output pin
// carries the full, untrimmed observation so nothing is lost.
type owmObservation struct {
	Dt   int64  `json:"dt"`
	Name string `json:"name"`
	Main struct {
		Temp      float64 `json:"temp"`
		FeelsLike float64 `json:"feels_like"`
		Pressure  int     `json:"pressure"`
		Humidity  int     `json:"humidity"`
	} `json:"main"`
	Wind struct {
		Speed float64 `json:"speed"`
		Deg   int     `json:"deg"`
	} `json:"wind"`
	Clouds struct {
		All int `json:"all"`
	} `json:"clouds"`
	Weather []owmWeather `json:"weather"`
}

// executeCurrent fetches the current conditions for the resolved coordinate
// and emits a readable summary, the bare temperature and conditions word, and
// the full observation as JSON.
func executeCurrent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := geoloc.ResolveLatLon(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if resolveKey(job) == "" {
		return params.Err(job, "not_connected", "OpenWeather isn't connected — add your API key on the OpenWeather integration page."), nil
	}

	status, body, err := owmGet(ctx, job, currentURL, lat, lon)
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var doc map[string]any
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		return params.ErrDetails(job, "owm_error", "OpenWeather returned an unexpected response.", uerr.Error()), nil
	}
	var obs owmObservation
	_ = json.Unmarshal(body, &obs) // best-effort: drives the human summary

	units := normalizeUnits(params.StringDefault(job.Params, "units", "metric"))
	conditions := ""
	if len(obs.Weather) > 0 {
		conditions = obs.Weather[0].Main
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary":     {MIME: "text/plain", Inline: currentSummary(obs, units)},
			"temperature": {MIME: "text/plain", Inline: geoloc.Num1(obs.Main.Temp)},
			"conditions":  {MIME: "text/plain", Inline: conditions},
			"weather":     {MIME: "application/json", Inline: doc},
		},
	}, nil
}

// currentSummary renders the current conditions as one human line, e.g.
// "Clear sky, 12.3°C (feels 11.1°C), humidity 64%, wind 3.4 m/s".
func currentSummary(o owmObservation, units string) string {
	desc := ""
	if len(o.Weather) > 0 {
		desc = geoloc.CapitalizeFirst(o.Weather[0].Description)
	}
	tu, su := geoloc.TempUnit(units), geoloc.SpeedUnit(units)
	if desc == "" {
		return fmt.Sprintf("%s%s (feels %s%s), humidity %d%%, wind %s %s",
			geoloc.Num1(o.Main.Temp), tu, geoloc.Num1(o.Main.FeelsLike), tu, o.Main.Humidity, geoloc.Num1(o.Wind.Speed), su)
	}
	return fmt.Sprintf("%s, %s%s (feels %s%s), humidity %d%%, wind %s %s",
		desc, geoloc.Num1(o.Main.Temp), tu, geoloc.Num1(o.Main.FeelsLike), tu, o.Main.Humidity, geoloc.Num1(o.Wind.Speed), su)
}
