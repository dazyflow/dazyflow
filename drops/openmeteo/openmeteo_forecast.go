// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package openmeteo

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/geoloc"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// maxForecastDays is the horizon Open-Meteo's free Forecast API covers.
const maxForecastDays = 16

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "openmeteo_forecast",
			Version:     "1.0",
			Label:       "Open-Meteo",
			Subtitle:    "Daily forecast",
			Summary:     "Get a multi-day daily forecast at a coordinate from Open-Meteo — no API key for non-commercial use.",
			Description: "Look ahead up to 16 days for any point on the map using Open-Meteo's free forecast — no account or API key for personal, non-commercial use. Give it a coordinate — type the Latitude and Longitude, or connect a \"lat,lon\" value in from a Location/Geocode step — and choose how many days. It returns a readable day-by-day Summary plus the Daily array as JSON (min/max temperature, conditions, and chance of rain per day). For commercial use, add your Open-Meteo API key on the integration page and it switches to the paid endpoint.",
			Integration: "Open-Meteo",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/openmeteo.svg",
			Color:       "#f5a623",
			Provider:    "internal",
			Tags:        []string{"weather", "open-meteo", "openmeteo", "forecast", "daily", "temperature", "coordinate", "lat", "lon", "rain", "free", "no key"},
			Examples: []core.ParamsExample{
				{
					Title:  "7-day forecast for Stockholm",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686,"units":"metric","days":7}`),
				},
				{
					Title:  "Tomorrow's outlook for London",
					Params: json.RawMessage(`{"lat":51.5072,"lon":-0.1276,"units":"metric","days":2}`),
					Notes:  "days counts from today, so 2 covers today + tomorrow. Each Daily entry carries temp_min/max, a conditions word, and pop (chance of rain, 0..1).",
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
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "daily", Label: "Daily", MIME: []string{"application/json"}},
				{Port: "weather", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude of the point, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude of the point, -180..180. Overridden by the Coordinate input."},
					"days":{"type":"integer","minimum":1,"maximum":16,"default":7,"title":"Days","description":"How many days to return, counting from today (1..16)."},
					"units":{"type":"string","enum":["metric","imperial"],"enumNames":["Metric (°C, m/s)","Imperial (°F, mph)"],"default":"metric","title":"Units","description":"metric = °C + m/s, imperial = °F + mph."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeForecast,
	})
}

// omDailyResponse is the /v1/forecast daily block. Open-Meteo returns each
// variable as a parallel column array aligned by index with `time`.
type omDailyResponse struct {
	Daily struct {
		Time          []string  `json:"time"`
		WeatherCode   []int     `json:"weather_code"`
		TempMax       []float64 `json:"temperature_2m_max"`
		TempMin       []float64 `json:"temperature_2m_min"`
		PrecipProbMax []float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// dayEntry is one calendar day, flattened from the column arrays into a row.
type dayEntry struct {
	Date        string  `json:"date"` // local YYYY-MM-DD (timezone=auto)
	TempMin     float64 `json:"temp_min"`
	TempMax     float64 `json:"temp_max"`
	Pop         float64 `json:"pop"` // chance of rain, 0..1
	Conditions  string  `json:"conditions"`
	Description string  `json:"description"`
}

// executeForecast fetches the daily forecast for the resolved coordinate,
// flattens Open-Meteo's column arrays into per-day rows, trims to the
// requested number of days, and emits a readable summary plus the daily array
// (and the full raw response).
func executeForecast(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := geoloc.ResolveLatLon(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	days := min(max(params.IntDefault(job.Params, "days", 7), 1), maxForecastDays)

	units := normalizeUnits(params.StringDefault(job.Params, "units", "metric"))
	q := baseQuery(lat, lon, units)
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("forecast_days", strconv.Itoa(days))
	q.Set("timezone", "auto") // local calendar days

	status, body, err := omGet(ctx, job, q)
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var doc map[string]any
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		return params.ErrDetails(job, "openmeteo_error", "Open-Meteo returned an unexpected response.", uerr.Error()), nil
	}
	var typed omDailyResponse
	_ = json.Unmarshal(body, &typed) // best-effort: drives the aggregation

	daily := flattenDaily(typed, days)

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary": {MIME: "text/plain", Inline: forecastSummary(daily, units)},
			"daily":   {MIME: "application/json", Inline: daily},
			"weather": {MIME: "application/json", Inline: doc},
		},
	}, nil
}

// flattenDaily zips Open-Meteo's parallel column arrays into per-day rows,
// reading each variable defensively (a short column leaves that field zero) and
// trimming to the first `days`. Open-Meteo reports precipitation_probability as
// a percentage; it's normalised to 0..1 to match the other weather drops' pop.
func flattenDaily(r omDailyResponse, days int) []dayEntry {
	d := r.Daily
	n := min(len(d.Time), days)
	out := make([]dayEntry, 0, n)
	for i := range n {
		e := dayEntry{Date: d.Time[i]}
		if i < len(d.TempMin) {
			e.TempMin = d.TempMin[i]
		}
		if i < len(d.TempMax) {
			e.TempMax = d.TempMax[i]
		}
		if i < len(d.PrecipProbMax) {
			e.Pop = d.PrecipProbMax[i] / 100
		}
		if i < len(d.WeatherCode) {
			e.Conditions = classFor(d.WeatherCode[i])
			e.Description = wmo[d.WeatherCode[i]]
		}
		out = append(out, e)
	}
	return out
}

// forecastSummary renders one line per day, e.g.
// "Mon Jun 24: Slight rain, 9–19°C, rain 20%".
func forecastSummary(days []dayEntry, units string) string {
	if len(days) == 0 {
		return "No forecast available."
	}
	tu := geoloc.TempUnit(units)
	var b strings.Builder
	for _, d := range days {
		label := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			label = t.Format("Mon Jan 2")
		}
		desc := geoloc.CapitalizeFirst(d.Description)
		fmt.Fprintf(&b, "%s: ", label)
		if desc != "" {
			fmt.Fprintf(&b, "%s, ", desc)
		}
		fmt.Fprintf(&b, "%s–%s%s, rain %s%%\n", geoloc.Num0(d.TempMin), geoloc.Num0(d.TempMax), tu, geoloc.Num0(d.Pop*100))
	}
	return strings.TrimRight(b.String(), "\n")
}
