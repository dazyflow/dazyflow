// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package smhi

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

// maxForecastDays caps the horizon (SMHI's point forecast runs ~10 days).
const maxForecastDays = 10

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "smhi_forecast",
			Version:     "1.0",
			Label:       "SMHI Weather",
			Subtitle:    "Daily forecast",
			Summary:     "Get a multi-day daily forecast at a coordinate from SMHI — no API key.",
			Description: "Look ahead up to 10 days for a point in the Nordic region (and the surrounding area) using SMHI's free Open Data forecast — no account or API key. Give it a coordinate — type the Latitude and Longitude, or wire a \"lat,lon\" value in from a Location/Geocode step — and choose how many days. It returns a readable day-by-day Summary plus the Daily array as JSON (min/max temperature and conditions per day), in metric units.",
			Integration: "SMHI",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/smhi.svg",
			Color:       "#29a0d6",
			Provider:    "internal",
			Tags:        []string{"weather", "smhi", "forecast", "daily", "temperature", "coordinate", "lat", "lon", "sweden", "nordic", "no key"},
			Examples: []core.ParamsExample{
				{
					Title:  "5-day forecast for Stockholm",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686,"days":5}`),
				},
				{
					Title:  "Tomorrow's outlook for Kiruna",
					Params: json.RawMessage(`{"lat":67.8558,"lon":20.2253,"days":2}`),
					Notes:  "days counts from today (1..10). Each Daily entry carries temp_min/max and a conditions word.",
				},
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
					"days":{"type":"integer","minimum":1,"maximum":10,"default":5,"title":"Days","description":"How many days to return, counting from today (1..10)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeForecast,
	})
}

// smhiDay is one calendar day rolled up from the sub-daily forecast steps.
type smhiDay struct {
	Date        string  `json:"date"` // UTC YYYY-MM-DD
	TempMin     float64 `json:"temp_min"`
	TempMax     float64 `json:"temp_max"`
	Conditions  string  `json:"conditions"`
	Description string  `json:"description"`
}

func executeForecast(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := resolveCoord(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}
	days := min(max(params.IntDefault(job.Params, "days", 5), 1), maxForecastDays)

	status, body, err := smhiGet(ctx, job, lat, lon, false) // full series
	if f := httpFailure(job, status, body, err); f != nil {
		return *f, nil
	}

	var doc map[string]any
	if uerr := json.Unmarshal(body, &doc); uerr != nil {
		return params.ErrDetails(job, "smhi_error", "SMHI returned an unexpected response.", uerr.Error()), nil
	}
	var typed smhiResponse
	if uerr := json.Unmarshal(body, &typed); uerr != nil || len(typed.TimeSeries) == 0 {
		return params.Err(job, "smhi_error", "SMHI returned no forecast data for that coordinate."), nil
	}

	daily := aggregateDays(typed.TimeSeries, days)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary": {MIME: "text/plain", Inline: forecastSummary(daily)},
			"daily":   {MIME: "application/json", Inline: daily},
			"weather": {MIME: "application/json", Inline: doc},
		},
	}, nil
}

// aggregateDays buckets the sub-daily steps into UTC calendar days, taking the
// min/max temperature and the conditions of the step nearest 12:00 UTC. SMHI
// gives no timezone, so days are UTC — for the Nordics that lines up with local
// days closely enough.
func aggregateDays(ts []smhiEntry, days int) []smhiDay {
	idx := map[string]int{}
	noonDelta := map[string]int{}
	out := []smhiDay{}

	for _, e := range ts {
		if len(e.Time) < 13 {
			continue
		}
		date := e.Time[:10]
		hour, _ := strconv.Atoi(e.Time[11:13])

		pos, seen := idx[date]
		if !seen {
			pos = len(out)
			idx[date] = pos
			out = append(out, smhiDay{Date: date, TempMin: math.Inf(1), TempMax: math.Inf(-1)})
			noonDelta[date] = 1 << 30
		}
		d := &out[pos]
		if t, ok := e.num("air_temperature"); ok {
			if t < d.TempMin {
				d.TempMin = t
			}
			if t > d.TempMax {
				d.TempMax = t
			}
		}
		delta := hour - 12
		if delta < 0 {
			delta = -delta
		}
		if delta < noonDelta[date] {
			noonDelta[date] = delta
			if code, ok := e.num("symbol_code"); ok {
				d.Conditions = classFor(int(code))
				d.Description = wsymb2[int(code)]
			}
		}
	}

	for i := range out {
		if math.IsInf(out[i].TempMin, 0) {
			out[i].TempMin = 0
		}
		if math.IsInf(out[i].TempMax, 0) {
			out[i].TempMax = 0
		}
	}
	if len(out) > days {
		out = out[:days]
	}
	return out
}

// forecastSummary renders one line per day, e.g. "Mon Jun 24: Clear sky, 9–18°C".
func forecastSummary(days []smhiDay) string {
	if len(days) == 0 {
		return "No forecast available."
	}
	var b strings.Builder
	for _, d := range days {
		label := d.Date
		if t, err := time.Parse("2006-01-02", d.Date); err == nil {
			label = t.Format("Mon Jan 2")
		}
		fmt.Fprintf(&b, "%s: ", label)
		if desc := capitalizeFirst(d.Description); desc != "" {
			fmt.Fprintf(&b, "%s, ", desc)
		}
		fmt.Fprintf(&b, "%s–%s°C\n", num0(d.TempMin), num0(d.TempMax))
	}
	return strings.TrimRight(b.String(), "\n")
}
