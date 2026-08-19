// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// maxForecastDays is the horizon the free 5-day/3-hour forecast covers.
const maxForecastDays = 5

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "weather_forecast",
			Version:     "1.0",
			Label:       "Weather",
			Subtitle:    "Daily forecast",
			Summary:     "Get a multi-day daily forecast for a latitude/longitude from OpenWeather.",
			Description: "Look ahead a few days for a point on the map. Give it a coordinate — type the Latitude and Longitude, or wire a \"lat,lon\" value in from another step — and choose how many days (1–5). It returns a readable day-by-day Summary plus the Daily array as JSON (min/max temperature, conditions, and chance of rain per day), aggregated from OpenWeather's free 5-day/3-hour forecast. Any standard key works, no paid subscription.",
			Integration: "OpenWeather",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/openweather.svg",
			Color:       "#eb6e4b",
			Provider:    "internal",
			Tags:        []string{"weather", "openweather", "openweathermap", "forecast", "daily", "temperature", "coordinate", "lat", "lon", "rain"},
			Examples: []core.ParamsExample{
				{
					Title:  "5-day forecast for Stockholm",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686,"units":"metric","days":5}`),
				},
				{
					Title:  "Tomorrow's outlook for London",
					Params: json.RawMessage(`{"lat":51.5072,"lon":-0.1276,"units":"metric","days":2}`),
					Notes:  "days counts from today, so 2 covers today + tomorrow. Each Daily entry carries temp_min/max, a conditions word, and pop (chance of rain, 0..1).",
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
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "daily", Label: "Daily", MIME: []string{"application/json"}},
				{Port: "weather", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude of the point, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude of the point, -180..180. Overridden by the Coordinate input."},
					"days":{"type":"integer","minimum":1,"maximum":5,"default":5,"title":"Days","description":"How many days to return, counting from today (1..5)."},
					"units":{"type":"string","enum":["metric","imperial","standard"],"default":"metric","title":"Units","description":"metric = °C + m/s, imperial = °F + mph, standard = K + m/s."},
					"lang":{"type":"string","title":"Language","description":"Optional ISO language code for the per-day conditions (e.g. sv, de, es). Defaults to English."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeForecast,
	})
}

// owmForecast is the /data/2.5/forecast response: 3-hourly slots over 5 days
// plus a city block whose timezone offset lets us bucket slots into LOCAL days.
type owmForecast struct {
	List []owmSlot `json:"list"`
	City struct {
		Name     string `json:"name"`
		Timezone int    `json:"timezone"` // shift in seconds from UTC
	} `json:"city"`
}

// owmSlot is one 3-hour forecast step.
type owmSlot struct {
	Dt   int64 `json:"dt"`
	Main struct {
		Temp    float64 `json:"temp"`
		TempMin float64 `json:"temp_min"`
		TempMax float64 `json:"temp_max"`
	} `json:"main"`
	Pop     float64      `json:"pop"`
	Weather []owmWeather `json:"weather"`
}

// dayAgg is one calendar day rolled up from the 3-hourly slots that fall in it.
type dayAgg struct {
	Date        string  `json:"date"` // local YYYY-MM-DD
	Dt          int64   `json:"dt"`   // the representative (nearest-noon) slot
	TempMin     float64 `json:"temp_min"`
	TempMax     float64 `json:"temp_max"`
	Pop         float64 `json:"pop"` // max chance of rain across the day, 0..1
	Conditions  string  `json:"conditions"`
	Description string  `json:"description"`
}

// executeForecast fetches the 5-day/3-hour forecast for the resolved
// coordinate, aggregates the slots into per-day min/max + conditions, trims to
// the requested number of days, and emits a readable summary plus the daily
// array (and the full raw response).
func executeForecast(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := resolveCoord(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	if resolveKey(job) == "" {
		return params.Err(job, "not_connected", "OpenWeather isn't connected — add your API key on the OpenWeather integration page."), nil
	}

	days := min(max(params.IntDefault(job.Params, "days", 5), 1), maxForecastDays)

	status, body, err := owmGet(ctx, job, forecastURL, lat, lon)
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var doc map[string]any
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		return params.ErrDetails(job, "owm_error", "OpenWeather returned an unexpected response.", uerr.Error()), nil
	}
	var fc owmForecast
	_ = json.Unmarshal(body, &fc) // best-effort: drives the aggregation

	daily := aggregateDaily(fc, days)
	units := normalizeUnits(params.StringDefault(job.Params, "units", "metric"))

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

// aggregateDaily buckets the 3-hourly slots into local calendar days (using the
// city's UTC offset), then for each day takes the min/max temperature, the
// highest chance of rain, and the conditions of the slot nearest local noon —
// the representative "daytime" weather. Days come out in chronological order,
// trimmed to the first `days`.
func aggregateDaily(fc owmForecast, days int) []dayAgg {
	off := int64(fc.City.Timezone)
	idx := map[string]int{}
	bestNoonDelta := map[string]int64{}
	out := []dayAgg{}

	for _, s := range fc.List {
		localUnix := s.Dt + off
		day := time.Unix(localUnix, 0).UTC().Format("2006-01-02")

		pos, seen := idx[day]
		if !seen {
			pos = len(out)
			idx[day] = pos
			out = append(out, dayAgg{Date: day, Dt: s.Dt, TempMin: s.Main.TempMin, TempMax: s.Main.TempMax, Pop: s.Pop})
			bestNoonDelta[day] = 1 << 62
		}
		d := &out[pos]
		if s.Main.TempMin < d.TempMin {
			d.TempMin = s.Main.TempMin
		}
		if s.Main.TempMax > d.TempMax {
			d.TempMax = s.Main.TempMax
		}
		if s.Pop > d.Pop {
			d.Pop = s.Pop
		}
		// Seconds-of-day in local time; the slot closest to 12:00 wins the
		// day's representative conditions.
		sod := ((localUnix % 86400) + 86400) % 86400
		delta := sod - 43200
		if delta < 0 {
			delta = -delta
		}
		if delta < bestNoonDelta[day] {
			bestNoonDelta[day] = delta
			d.Dt = s.Dt
			if len(s.Weather) > 0 {
				d.Conditions = s.Weather[0].Main
				d.Description = s.Weather[0].Description
			}
		}
	}

	if len(out) > days {
		out = out[:days]
	}
	return out
}

// forecastSummary renders one line per day, e.g.
// "Mon Jun 24: Light rain, 9–19°C, rain 20%".
func forecastSummary(days []dayAgg, units string) string {
	if len(days) == 0 {
		return "No forecast available."
	}
	tu := tempUnit(units)
	var b strings.Builder
	for _, d := range days {
		label := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			label = t.Format("Mon Jan 2")
		}
		desc := capitalizeFirst(d.Description)
		fmt.Fprintf(&b, "%s: ", label)
		if desc != "" {
			fmt.Fprintf(&b, "%s, ", desc)
		}
		fmt.Fprintf(&b, "%s–%s%s, rain %s%%\n", num0(d.TempMin), num0(d.TempMax), tu, num0(d.Pop*100))
	}
	return strings.TrimRight(b.String(), "\n")
}
