// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package smhi

import (
	"context"
	"encoding/json"
	"fmt"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "smhi_current",
			Version:     "1.0",
			Label:       "SMHI Weather",
			Subtitle:    "Current conditions",
			Summary:     "Get the current weather at a coordinate from SMHI — no API key.",
			Description: "Look up the weather right now for a point in the Nordic region (and the surrounding area) using SMHI's free Open Data forecast — no account or API key. Give it a coordinate — type the Latitude and Longitude, or wire a \"lat,lon\" value in from a Location/Geocode step — and it returns a one-line Summary plus the Temperature and a Conditions word (Clear, Rain, Snow…) you can branch on, all in metric units.",
			Integration: "SMHI",
			Category:    "network",
			Icon:        "cloud-sun",
			BrandLogo:   "/brands/smhi.svg",
			Color:       "#29a0d6",
			Provider:    "internal",
			Tags:        []string{"weather", "smhi", "forecast", "temperature", "coordinate", "lat", "lon", "current", "conditions", "sweden", "nordic", "no key"},
			Examples: []core.ParamsExample{
				{
					Title:  "Weather in Stockholm",
					Params: json.RawMessage(`{"lat":59.3293,"lon":18.0686}`),
				},
				{
					Title:  "Weather in Göteborg",
					Params: json.RawMessage(`{"lat":57.7089,"lon":11.9746}`),
					Notes:  "SMHI is always metric (°C, m/s). Conditions is a single word (Clear, Clouds, Rain…) that's easy to branch on.",
				},
			},
			ExecutionModel: core.ExecutionBatch,
			ProcessModel:   core.ProcessLongLived,
			Inputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "summary", Label: "Summary", MIME: []string{"text/plain"}},
				{Port: "temperature", Label: "Temperature", MIME: []string{"text/plain"}},
				{Port: "conditions", Label: "Conditions", MIME: []string{"text/plain"}},
				{Port: "weather", Label: "Full response", MIME: []string{"application/json"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"lat":{"type":"number","minimum":-90,"maximum":90,"title":"Latitude","description":"Latitude of the point, -90..90. Overridden by a \"lat,lon\" value on the Coordinate input."},
					"lon":{"type":"number","minimum":-180,"maximum":180,"title":"Longitude","description":"Longitude of the point, -180..180. Overridden by the Coordinate input."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for the request, in milliseconds."}
				}
			}`),
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeCurrent,
	})
}

// executeCurrent fetches just the current step (timeseries=1) and emits a
// readable summary, the bare temperature and conditions word, and the full
// response as JSON.
func executeCurrent(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	lat, lon, err := resolveCoord(job)
	if err != nil {
		return params.Err(job, "bad_param", err.Error()), nil
	}

	status, body, err := smhiGet(ctx, job, lat, lon, true)
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
	now := typed.TimeSeries[0]

	temp, _ := now.num("air_temperature")
	cond := ""
	if code, ok := now.num("symbol_code"); ok {
		cond = classFor(int(code))
	}

	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"summary":     {MIME: "text/plain", Inline: currentSummary(now)},
			"temperature": {MIME: "text/plain", Inline: num1(temp)},
			"conditions":  {MIME: "text/plain", Inline: cond},
			"weather":     {MIME: "application/json", Inline: doc},
		},
	}, nil
}

// currentSummary renders one human line, e.g.
// "Variable cloudiness, 21.7°C, humidity 55%, wind 2.3 m/s".
func currentSummary(e smhiEntry) string {
	desc := ""
	if code, ok := e.num("symbol_code"); ok {
		desc = capitalizeFirst(wsymb2[int(code)])
	}
	temp, _ := e.num("air_temperature")
	hum, _ := e.num("relative_humidity")
	wind, _ := e.num("wind_speed")
	if desc == "" {
		return fmt.Sprintf("%s°C, humidity %s%%, wind %s m/s", num1(temp), num0(hum), num1(wind))
	}
	return fmt.Sprintf("%s, %s°C, humidity %s%%, wind %s m/s", desc, num1(temp), num0(hum), num1(wind))
}
