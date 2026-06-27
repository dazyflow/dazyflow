// SPDX-FileCopyrightText: 2026 Joachim Klahr
// SPDX-License-Identifier: AGPL-3.0-or-later

package geo

import (
	"context"
	"encoding/json"
	"strings"

	"git.sr.ht/~klahr/dazyflow/core"
	"git.sr.ht/~klahr/dazyflow/drops/internal/params"
	"git.sr.ht/~klahr/dazyflow/engine"
)

func init() {
	engine.Register(engine.NativeDrop{
		Manifest: core.Manifest{
			ID:          "geo_location",
			Version:     "1.0",
			Label:       "Location",
			Subtitle:    "Map or place → coordinate",
			Summary:     "Pick a point on a map, or look up a place, and emit its coordinate.",
			Description: "Choose a location and emit its Coordinate (\"lat,lon\") for a Weather lookup. Drop a pin on the OpenStreetMap map right on the card (search, click, or drag). Or give it a Place — a city or address — typed in or wired from another step (a form field, a message): when a Place is set it's geocoded and OVERRIDES the map pin. So the map is the default, and the Place input wins when present. Uses OpenStreetMap — no account or key.",
			Integration: "OpenStreetMap",
			Category:    "network",
			Icon:        "map-pin",
			BrandLogo:   "/brands/openstreetmap.svg",
			Color:       "#7ebc6f",
			Provider:    "internal",
			Tags:        []string{"location", "coordinate", "map", "openstreetmap", "osm", "place", "city", "address", "geocode", "lat", "lon", "pin", "geo"},
			Examples: []core.ParamsExample{
				{
					Title:  "Pick a point on the map",
					Params: json.RawMessage(`{"point":"59.3293,18.0686"}`),
				},
				{
					Title:  "Look up a place instead",
					Params: json.RawMessage(`{"place":"Paris, France"}`),
					Notes:  "When Place is set (typed here or wired into the Place input) it's geocoded and overrides the map pin.",
				},
			},
			// Optional per-tenant geocoding backend. All fields optional —
			// unset means the keyless OpenStreetMap default, so the drop works
			// out of the box; configure to self-host or use LocationIQ.
			ConnectionFields: geoConnectionFields,
			ExecutionModel:   core.ExecutionBatch,
			ProcessModel:     core.ProcessLongLived,
			Inputs: []core.Port{
				// Place (a city or address) overrides the map pin when set.
				{Port: "place", Label: "Place", MIME: []string{"text/plain"}},
			},
			Outputs: []core.Port{
				{Port: "coordinate", Label: "Coordinate", MIME: []string{"text/plain"}},
				{Port: "lat", Label: "Latitude", MIME: []string{"text/plain"}},
				{Port: "lon", Label: "Longitude", MIME: []string{"text/plain"}},
				{Port: "place", Label: "Place", MIME: []string{"text/plain"}},
			},
			ParamsSchema: json.RawMessage(`{
				"type":"object",
				"properties":{
					"point":{"type":"string","format":"geo-point","title":"Location","description":"The map pin as \"lat,lon\". Use the map to set it. Overridden when a Place is provided."},
					"show_map":{"type":"boolean","default":true,"title":"Show map on card","description":"Show the interactive map on the node card. Turn off for a compact card — you can still pick in the inspector or drive it with the Place input."},
					"place":{"type":"string","title":"Place","description":"A city or address. When set, it's geocoded and overrides the map pin. The Place input overrides this typed value."},
					"countrycodes":{"type":"string","title":"Country bias","description":"Optional comma-separated ISO country codes to bias geocoding of the Place (e.g. \"us\" or \"se,no\")."},
					"language":{"type":"string","title":"Language","description":"Optional ISO language code for the returned place name (e.g. sv, de)."},
					"timeout_ms":{"type":"integer","default":15000,"minimum":1,"description":"Hard deadline for a Place lookup, in milliseconds."}
				}
			}`),
			// Reading a location has no side effects — safe to retry on a blip.
			Idempotent:  true,
			RetryPolicy: core.RetryExponentialBackoff,
		},
		Execute: executeLocation,
	})
}

// executeLocation emits a coordinate from either a Place (a city/address that
// gets geocoded) or the map pin. Precedence: the Place input wins, then the
// typed Place param, then the map pin. A Place, when present, always overrides
// the pin.
func executeLocation(ctx context.Context, job core.Job, _ chan<- core.Progress) (core.Result, error) {
	place, ok := params.TextInputOr(job, "place", params.StringDefault(job.Params, "place", ""))
	if !ok {
		return params.Err(job, "bad_input", "'Place' input must be text (a city or address)"), nil
	}
	place = strings.TrimSpace(place)

	var coord, placeName string
	if place != "" {
		// A Place is set → geocode it through the active backend; it
		// overrides the map pin.
		hit, errRes := geocoderFor(job).forward(ctx, job, place)
		if errRes != nil {
			return *errRes, nil
		}
		coord, placeName = hit.Coord, hit.DisplayName
	} else {
		// No Place → fall back to the map pin.
		point := strings.TrimSpace(params.StringDefault(job.Params, "point", ""))
		if point == "" {
			return params.Err(job, "bad_param", "set a Place (a city or address) or pick a point on the map"), nil
		}
		lat, lon, perr := parseLatLon(point)
		if perr != nil {
			return params.Err(job, "bad_param", perr.Error()), nil
		}
		coord = fmtCoord(lat, lon)
	}

	parts := strings.SplitN(coord, ",", 2)
	return core.Result{
		JobID:  job.ID,
		Status: core.StatusOK,
		Output: map[string]core.Ref{
			"coordinate": {MIME: "text/plain", Inline: coord},
			"lat":        {MIME: "text/plain", Inline: parts[0]},
			"lon":        {MIME: "text/plain", Inline: parts[1]},
			"place":      {MIME: "text/plain", Inline: placeName},
		},
	}, nil
}
